package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"google.golang.org/api/idtoken"
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
	// 1. Extract Token from Query Param
	token := r.URL.Query().Get("token")
	if token == "" {
		// Fallback to Bearer header if needed, but Query is easier for WS
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	if token == "" {
		http.Error(w, "Missing authentication token", http.StatusUnauthorized)
		slog.Warn("Connection attempt without token", "remote_addr", r.RemoteAddr)
		return
	}

	// 2. Verify Token
	ctx := context.Background()
	// validating for any audience for now, as we might have multiple client IDs (iOS, Web, Android)
	// Passing empty string as audience skips audience check, which we can refine later if needed.
	payload, err := idtoken.Validate(ctx, token, "")
	if err != nil {
		slog.Error("Token validation failed", "error", err, "remote_addr", r.RemoteAddr)
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	// 3. Extract Unique User ID (sub)
	userID := payload.Subject
	if userID == "" {
		slog.Error("Token payload missing subject", "remote_addr", r.RemoteAddr)
		http.Error(w, "Invalid token claims", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	clientID := userID
	client := &Client{ID: clientID, Conn: conn}

	clientsMu.Lock()
	clients[clientID] = client
	clientsMu.Unlock()

	slog.Info("Client connected (Authenticated)", "client_id", clientID)

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
	var pending *Client
	for {
		client := <-matchQueue

		// Check if the popped client is still connected
		if !isClientActive(client) {
			slog.Info("Skipping disconnected client", "client_id", client.ID)
			continue
		}

		if pending == nil {
			pending = client
			continue
		}

		// We have a pending client. Check if they are STILL connected.
		// (They might have disconnected while we waited for the second client)
		if !isClientActive(pending) {
			slog.Info("Pending client disconnected, replacing with new client", "old_pending", pending.ID, "new_pending", client.ID)
			pending = client
			continue
		}

		// Prevent matching with self
		if pending.ID == client.ID {
			slog.Info("Skipping self-match", "client_id", client.ID)
			continue
		}

		c1 := pending
		c2 := client

		slog.Info("Matching clients", "client1", c1.ID, "client2", c2.ID)

		// Notify both clients they are matched
		err1 := c1.Conn.WriteJSON(Message{
			Type:    "match",
			Payload: c2.ID,
		})
		if err1 != nil {
			slog.Error("Failed to send match to pending client", "client_id", c1.ID, "error", err1)
			// c1 is dead. c2 should be the new pending.
			pending = c2
			continue
		}

		err2 := c2.Conn.WriteJSON(Message{
			Type:    "match",
			Payload: c1.ID,
		})
		if err2 != nil {
			slog.Error("Failed to send match to new client", "client_id", c2.ID, "error", err2)
			// c2 is dead. c1 thinks it matched with c2, but c2 won't respond.
			// Ideally we might tell c1 "nevermind", but for now just log it.
			// c1 will likely timeout or disconnect.
		}

		// Reset pending
		pending = nil
	}
}

func isClientActive(c *Client) bool {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	activeClient, ok := clients[c.ID]
	return ok && activeClient == c
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
