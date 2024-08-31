package main

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Error while upgrading connection: %v", err)
			return
		}
		handleClient(conn)
	})

	serverAddr := "localhost:8765"
	log.Printf("WebSocket server started on ws://%s", serverAddr)
	if err := http.ListenAndServe(serverAddr, nil); err != nil {
		log.Fatalf("Error starting server: %v", err)
	}
}
