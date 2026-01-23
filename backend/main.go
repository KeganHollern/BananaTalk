package main

import (
	"log/slog"
	"net/http"
	"os"
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
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	http.HandleFunc("/ws", handleConnections)
	http.HandleFunc("/", handleNotFound)

	port := ":8080"
	// Start matching loop
	go matchingLoop()

	slog.Info("BananaTalk Backend starting", "port", port)
	err := http.ListenAndServe(port, nil)
	if err != nil {
		slog.Error("ListenAndServe failed", "error", err)
		os.Exit(1)
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// For Phase 1, we'll just use the RemoteAddr as a simple ID
	clientID := conn.RemoteAddr().String()
	client := &Client{ID: clientID, Conn: conn}

	clientsMu.Lock()
	clients[clientID] = client
	clientsMu.Unlock()

	slog.Info("Client connected", "client_id", clientID)

	// Send ID to client
	client.Conn.WriteJSON(Message{
		Type:    "init",
		Payload: clientID,
	})

	// Add to match queue
	slog.Info("Adding client to match queue", "client_id", clientID)
	matchQueue <- client

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			slog.Error("ReadJSON error", "client_id", clientID, "error", err)
			break
		}

		msg.From = clientID
		handleMessage(msg)
	}

	clientsMu.Lock()
	delete(clients, clientID)
	clientsMu.Unlock()
	slog.Info("Client disconnected", "client_id", clientID)
}

func matchingLoop() {
	for {
		c1 := <-matchQueue
		c2 := <-matchQueue

		slog.Info("Matching clients", "client1", c1.ID, "client2", c2.ID)

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
			slog.Error("Failed to send message", "to", msg.To, "error", err)
		}
	}
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		slog.Info("Redirecting 404", "path", r.URL.Path, "remote_addr", r.RemoteAddr)
	}
	http.Redirect(w, r, "https://lystic.dev", http.StatusFound)
}
