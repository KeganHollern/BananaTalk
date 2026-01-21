package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

type Message struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	To      string      `json:"to,omitempty"`
	From    string      `json:"from,omitempty"`
}

type Client struct {
	ID   string
	Conn *websocket.Conn
}

var (
	clients    = make(map[string]*Client)
	clientsMu  sync.Mutex
	matchQueue = make(chan *Client, 100)
)

func main() {
	http.HandleFunc("/ws", handleConnections)

	port := ":8080"
	// Start matching loop
	go matchingLoop()

	err := http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer conn.Close()

	// For Phase 1, we'll just use the RemoteAddr as a simple ID
	clientID := conn.RemoteAddr().String()
	client := &Client{ID: clientID, Conn: conn}

	clientsMu.Lock()
	clients[clientID] = client
	clientsMu.Unlock()

	log.Printf("Client connected: %s", clientID)

	// Send ID to client
	client.Conn.WriteJSON(Message{
		Type:    "init",
		Payload: clientID,
	})

	// Add to match queue
	matchQueue <- client

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("error: %v", err)
			break
		}

		msg.From = clientID
		handleMessage(msg)
	}

	clientsMu.Lock()
	delete(clients, clientID)
	clientsMu.Unlock()
	log.Printf("Client disconnected: %s", clientID)
}

func matchingLoop() {
	for {
		c1 := <-matchQueue
		c2 := <-matchQueue

		log.Printf("Matching %s with %s", c1.ID, c2.ID)

		// Notify both clients they are matched
		c1.Conn.WriteJSON(Message{
			Type:    "match",
			Payload: c2.ID,
		})
		c2.Conn.WriteJSON(Message{
			Type:    "match",
			Payload: c1.ID,
		})
	}
}

func handleMessage(msg Message) {
	if msg.To == "" {
		return
	}

	clientsMu.Lock()
	target, ok := clients[msg.To]
	clientsMu.Unlock()

	if ok {
		err := target.Conn.WriteJSON(msg)
		if err != nil {
			log.Printf("Failed to send message to %s: %v", msg.To, err)
		}
	}
}
