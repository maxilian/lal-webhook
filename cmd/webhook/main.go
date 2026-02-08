package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

// --- Configuration Structs ---

type LalNotify struct {
	SessionID  string `json:"session_id"`
	ID         string `json:"id"`
	StreamName string `json:"stream_name"`
	Url        string `json:"url"`
}

type KickReq struct {
	StreamName string `json:"stream_name"`
	SessionId  string `json:"session_id"`
}

func (l *LalNotify) GetID() string {
	if l.SessionID != "" {
		return l.SessionID
	}
	return l.ID
}

// --- Globals ---

var (
	ctx          = context.Background()
	rdb          *redis.Client
	LalApiAddr   string
	DefaultQuota int
)

// --- Initialization ---

func init() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	LalApiAddr = getEnv("LAL_API_ADDR", "127.0.0.1:8083")
	quotaStr := getEnv("DEFAULT_QUOTA_SEC", "120")
	DefaultQuota, _ = strconv.Atoi(quotaStr)

	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	redisPass := getEnv("REDIS_PASSWORD", "")
	redisDB, _ := strconv.Atoi(getEnv("REDIS_DB", "0"))

	rdb = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPass,
		DB:       redisDB,
		PoolSize: 100, // Optimized for high-throughput m6a.large
	})
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// --- Core Logic ---

// kickLalSession sends the JSON POST request to LAL Control API
func kickLalSession(streamName, sessionId string) {
	apiUrl := fmt.Sprintf("http://%s/api/ctrl/kick_session", LalApiAddr)
	payload := KickReq{
		StreamName: streamName,
		SessionId:  sessionId,
	}

	jsonBody, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", apiUrl, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("!!! KICK HTTP ERROR: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("LAL API RESPONSE [SID: %s]: %s", sessionId, string(body))
}

// startEnforcer runs every 5 seconds to deduct quota and kick users
func startEnforcer() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		keys, _ := rdb.Keys(ctx, "active_sids:*").Result()
		month := time.Now().Format("2006-01")

		for _, key := range keys {
			streamName := strings.TrimPrefix(key, "active_sids:")
			sids, _ := rdb.SMembers(ctx, key).Result()

			for _, sID := range sids {
				token, _ := rdb.Get(ctx, "sid_to_token:"+sID).Result()
				if token == "" {
					continue
				}

				tokenKey := fmt.Sprintf("remain:%s:%s", token, month)
				newBalance, _ := rdb.DecrBy(ctx, tokenKey, 5).Result()

				if newBalance <= 0 {
					log.Printf("FORCE KICK: Token %s balance %d.", token, newBalance)

					// Kick immediately
					kickLalSession(streamName, sID)

					// Cleanup Redis to prevent double-kicking
					rdb.SRem(ctx, key, sID)
					rdb.Del(ctx, "sid_to_token:"+sID)
				} else {
					log.Printf("QUOTA CHECK: Token %s has %ds left", token, newBalance)
				}
			}
		}
	}
}

// --- Main Webhook Server ---

func main() {
	// 1. Set Gin to production mode for better performance
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// 2. Webhook: Subscriber Start
	r.POST("/on_sub_start", func(c *gin.Context) {
		var msg LalNotify
		if err := c.ShouldBindJSON(&msg); err != nil {
			c.JSON(200, gin.H{"error_code": 1, "desp": "invalid json"})
			return
		}

		u, _ := url.Parse(msg.Url)
		token := u.Query().Get("token")
		sID := msg.GetID()

		// Logic for NO TOKEN
		if token == "" {
			log.Printf("SECURITY: Missing token for SID %s", sID)
			c.JSON(200, gin.H{"error_code": 1001, "desp": "token required"})

			// Fallback kick
			go func() {
				time.Sleep(300 * time.Millisecond)
				kickLalSession(msg.StreamName, sID)
			}()
			return
		}

		month := time.Now().Format("2006-01")
		tokenKey := fmt.Sprintf("remain:%s:%s", token, month)

		// Check and initialize quota
		rdb.SetNX(ctx, tokenKey, DefaultQuota, 0)
		remain, _ := rdb.Get(ctx, tokenKey).Int()

		if remain <= 0 {
			log.Printf("GATEKEEPER: Rejecting %s (Balance: %d)", token, remain)
			c.JSON(200, gin.H{"error_code": 1002, "desp": "out of quota"})

			// Secondary safety kick
			go func() {
				time.Sleep(300 * time.Millisecond)
				kickLalSession(msg.StreamName, sID)
			}()
			return
		}

		// Map session to token
		rdb.Set(ctx, "sid_to_token:"+sID, token, 2*time.Hour)
		rdb.SAdd(ctx, "active_sids:"+msg.StreamName, sID)

		log.Printf("GATEKEEPER: Allowed %s (SID: %s, Balance: %d)", token, sID, remain)
		c.JSON(200, gin.H{"error_code": 0, "desp": "ok"})
	})

	// 3. Webhook: Subscriber Stop
	r.POST("/on_sub_stop", func(c *gin.Context) {
		var msg LalNotify
		c.ShouldBindJSON(&msg)
		sID := msg.GetID()

		rdb.SRem(ctx, "active_sids:"+msg.StreamName, sID)
		rdb.Del(ctx, "sid_to_token:"+sID)

		log.Printf("CLEANUP: SID %s disconnected", sID)
		c.JSON(200, gin.H{"error_code": 0})
	})

	// 4. API: View Quotas
	r.GET("/quotas", func(c *gin.Context) {
		month := time.Now().Format("2006-01")
		keys, _ := rdb.Keys(ctx, "remain:*:"+month).Result()

		results := make(map[string]int)
		for _, k := range keys {
			val, _ := rdb.Get(ctx, k).Int()
			results[k] = val
		}
		c.JSON(200, results)
	})

	// Start Enforcer and Web Server
	go startEnforcer()

	port := getEnv("APP_PORT", "5000")
	fmt.Printf("\n🚀 webhook running on port %s\n\n", port)
	r.Run(":" + port)
}
