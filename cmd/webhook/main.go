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
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

var (
	ctx          = context.Background()
	rdb          = redis.NewClient(&redis.Options{Addr: "localhost:6379", PoolSize: 100})
	LalApiAddr   = "127.0.0.1:8083"
	DefaultQuota = 120
)

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

// --- THE KICK FUNCTION (Matches your successful CURL) ---
func kickLalSession(streamName, sessionId string) {
	apiUrl := fmt.Sprintf("http://%s/api/ctrl/kick_session", LalApiAddr)

	payload := KickReq{
		StreamName: streamName,
		SessionId:  sessionId,
	}

	jsonBody, _ := json.Marshal(payload)

	// Create request manually to ensure headers match CURL exactly
	req, _ := http.NewRequest("POST", apiUrl, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("!!! HTTP ERROR: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("LAL KICK RESPONSE [SID: %s]: %s", sessionId, string(body))
}

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

				// Deduct 5 seconds
				newBalance, _ := rdb.DecrBy(ctx, tokenKey, 5).Result()

				if newBalance <= 0 {
					log.Printf("QUOTA EXHAUSTED (%d). Kicking Token: %s", newBalance, token)
					kickLalSession(streamName, sID)

					// Cleanup Redis
					rdb.SRem(ctx, key, sID)
					rdb.Del(ctx, "sid_to_token:"+sID)
				}
			}
		}
	}
}

func main() {
	r := gin.Default()

	r.POST("/on_sub_start", func(c *gin.Context) {
		var msg LalNotify
		if err := c.ShouldBindJSON(&msg); err != nil {
			return
		}

		u, _ := url.Parse(msg.Url)
		token := u.Query().Get("token")
		sID := msg.GetID()

		if token == "" {
			log.Printf("SECURITY: No token provided for stream %s. Rejecting SID %s", msg.StreamName, sID)

			// Return error to LAL (This stops the handshake)
			c.JSON(200, gin.H{"error_code": 1001, "desp": "token required"})

			// FORCE KICK (The "Hammer"):
			// We use a goroutine to wait a tiny bit for LAL to register the session, then kill it.
			go func(stream, sid string) {
				time.Sleep(200 * time.Millisecond)
				kickLalSession(stream, sid)
			}(msg.StreamName, sID)

			return
		}

		month := time.Now().Format("2006-01")
		tokenKey := fmt.Sprintf("remain:%s:%s", token, month)

		// 1. Initialize if new
		rdb.SetNX(ctx, tokenKey, DefaultQuota, 0)

		// 2. CHECK QUOTA
		remain, _ := rdb.Get(ctx, tokenKey).Int()
		if remain <= 0 {
			log.Printf("GATEKEEPER REJECT: %s is at %d", token, remain)

			// If for some reason they bypassed and are trying to start, kick immediately
			go func() {
				time.Sleep(500 * time.Millisecond) // Wait for LAL to finish register
				kickLalSession(msg.StreamName, sID)
			}()

			c.JSON(200, gin.H{"error_code": 1002, "desp": "no quota"})
			return
		}

		// 3. REGISTER
		rdb.Set(ctx, "sid_to_token:"+sID, token, 2*time.Hour)
		rdb.SAdd(ctx, "active_sids:"+msg.StreamName, sID)

		log.Printf("GATEKEEPER ALLOW: %s (Balance: %d, SID: %s)", token, remain, sID)
		c.JSON(200, gin.H{"error_code": 0})
	})

	r.POST("/on_sub_stop", func(c *gin.Context) {
		var msg LalNotify
		c.ShouldBindJSON(&msg)
		sID := msg.GetID()
		rdb.SRem(ctx, "active_sids:"+msg.StreamName, sID)
		rdb.Del(ctx, "sid_to_token:"+sID)
		c.JSON(200, gin.H{"error_code": 0})
	})

	r.GET("/quotas", func(c *gin.Context) {
		// 1. Find all keys matching the remain pattern
		pattern := "remain:*:2026-02"
		keys, _ := rdb.Keys(ctx, pattern).Result()

		type QuotaInfo struct {
			Stream    string `json:"stream"`
			Remaining int    `json:"remaining_sec"`
			Status    string `json:"status"`
		}

		var results []QuotaInfo
		for _, key := range keys {
			val, _ := rdb.Get(ctx, key).Int()

			// Extract stream name from key "remain:streamName:2026-02"
			// (Splitting by ":" is safe here)
			status := "active"
			if val <= 0 {
				status = "exhausted"
			}

			results = append(results, QuotaInfo{
				Stream:    key,
				Remaining: val,
				Status:    status,
			})
		}

		c.JSON(200, results)
	})

	go startEnforcer()
	r.Run(":5000")
}
