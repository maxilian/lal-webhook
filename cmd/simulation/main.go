package main

import (
	"log"
	"net/url"
	"os"
	"os/signal"

	"github.com/gorilla/websocket"
)

func main() {
	// Connect as 'company_abc'
	u := url.URL{
		Scheme:   "ws",
		Host:     "localhost:8080",
		Path:     "/live/testing.flv",
		RawQuery: "token=user_token_xyz",
	}

	log.Printf("Connecting to %s", u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("Dial failed:", err)
	}
	defer c.Close()

	log.Println("Streaming... Press Ctrl+C to stop and trigger on_stop")

	// Monitor binary data
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	for {
		select {
		case <-interrupt:
			log.Println("Stopping stream...")
			return
		default:
			_, msg, err := c.ReadMessage()
			if err != nil {
				log.Println("Server closed connection:", err)
				return
			}
			log.Printf("Received %d bytes", len(msg))
		}
	}
}
