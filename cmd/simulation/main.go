package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

// Config holds all environment-based settings
type Config struct {
	StreamURL  string
	Token      string
	Concurrent int
}

// LoadConfig reads from .env or system environment
func LoadConfig() *Config {
	_ = godotenv.Load() // optional load

	concurrent, _ := strconv.Atoi(getEnv("CONCURRENT_VIEWERS", "10"))
	return &Config{
		StreamURL:  getEnv("TEST_STREAM_URL", "ws://localhost:8080/live/testing.flv"),
		Token:      getEnv("TEST_TOKEN", "user_token_xyz"),
		Concurrent: concurrent,
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// --- Viewer Logic ---

func runViewer(ctx context.Context, id int, cfg *Config, wg *sync.WaitGroup) {
	defer wg.Done()

	// Construct URL with Token
	u, err := url.Parse(cfg.StreamURL)
	if err != nil {
		log.Printf("[%d] Invalid URL: %v", id, err)
		return
	}
	q := u.Query()
	q.Set("token", cfg.Token)
	u.RawQuery = q.Encode()

	log.Printf("🚀 [Viewer %03d] Dialing...", id)

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("❌ [Viewer %03d] Connection Failed: %v", id, err)
		return
	}
	defer conn.Close()

	// Data tracking
	var totalBytes int64

	for {
		select {
		case <-ctx.Done():
			log.Printf("🛑 [Viewer %03d] Shutting down. Received: %d KB", id, totalBytes/1024)
			return
		default:
			// Read stream data
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Printf("⚠️ [Viewer %03d] Kicked or Lost: %v", id, err)
				return
			}
			totalBytes += int64(len(msg))

			// Optional: print heartbeat for first viewer only to keep logs clean
			if id == 1 && totalBytes%102400 == 0 {
				log.Printf("📺 [Viewer 1] Buffering... (Total: %d KB)", totalBytes/1024)
			}
		}
	}
}

// --- Main Execution ---

func main() {
	cfg := LoadConfig()

	// Set up Context with Cancellation for clean shutdown
	ctx, cancel := context.WithCancel(context.Background())

	// Handle OS Signals (Ctrl+C)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	var wg sync.WaitGroup

	fmt.Printf(`
|==========================================|
|   LAL SERVER LOAD TESTER (CONCURRENT)    |
|==========================================|
| Target: %s
| Users:  %d
| Token:  %s
|==========================================|
`, cfg.StreamURL, cfg.Concurrent, cfg.Token)

	// Launch concurrent viewers
	for i := 1; i <= cfg.Concurrent; i++ {
		wg.Add(1)
		go runViewer(ctx, i, cfg, &wg)

		// Staggered startup to avoid CPU/Network burst on m6a.large
		time.Sleep(50 * time.Millisecond)
	}

	// Block until Ctrl+C
	<-sigChan
	fmt.Println("\n[!] Shutdown signal received. Closing all connections...")
	cancel() // Notifies all goroutines via ctx.Done()

	// Wait for all goroutines to cleanup
	wg.Wait()
	fmt.Println("[✔] Test completed successfully.")
}
