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

// MatchMaker manages the matching queue
type MatchMaker struct {
	queue  []*Client
	mu     sync.Mutex
	notify chan struct{}
}

func NewMatchMaker() *MatchMaker {
	return &MatchMaker{
		queue:  make([]*Client, 0),
		notify: make(chan struct{}, 1),
	}
}

func (m *MatchMaker) Add(c *Client) {
	m.mu.Lock()
	m.queue = append(m.queue, c)
	m.mu.Unlock()
	slog.Info("Adding client to match queue", "client_id", c.ID)

	// Non-blocking send to trigger loop
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *MatchMaker) Remove(c *Client) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, client := range m.queue {
		if client.ID == c.ID {
			// Remove from slice
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			slog.Info("Removed client from match queue", "client_id", c.ID)
			return
		}
	}
}

func (m *MatchMaker) Run() {
	for {
		<-m.notify

		m.mu.Lock()
		if len(m.queue) < 2 {
			m.mu.Unlock()
			continue
		}

		c1 := m.queue[0]
		c2 := m.queue[1]
		m.queue = m.queue[2:]
		m.mu.Unlock()

		slog.Info("Matching clients", "client1", c1.ID, "client2", c2.ID)

		// Notify both clients they are matched
		err1 := c1.Conn.WriteJSON(Message{
			Type:    "match",
			Payload: c2.ID,
		})
		
		err2 := c2.Conn.WriteJSON(Message{
			Type:    "match",
			Payload: c1.ID,
		})

		// If c1 failed, c2 is orphaned (unless c2 also failed). 
		// For simplicity, if we fail to write to one, the other gets a match message 
		// but the peer won't respond. The active client will eventually disconnect.
		// A more robust solution might re-queue the survivor, but this is a starter fix.
		if err1 != nil {
			slog.Error("Failed to send match to client 1", "client_id", c1.ID, "error", err1)
		}
		if err2 != nil {
			slog.Error("Failed to send match to client 2", "client_id", c2.ID, "error", err2)
		}

		// Check if we still have enough people to run again immediately
		m.mu.Lock()
		if len(m.queue) >= 2 {
			select {
			case m.notify <- struct{}{}:
			default:
			}
		}
		m.mu.Unlock()
	}
}

var (
	clients    = make(map[string]*Client)
	clientsMu  sync.Mutex
	matchMaker = NewMatchMaker()
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	http.HandleFunc("/ws", handleConnections)
	http.HandleFunc("/", handleNotFound)

	port := ":8080"
	// Start matching loop
	go matchMaker.Run()

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
		// Explicitly logging it as expired/invalid for clarity
		slog.Info("JWT Token expired or invalid", "token_snippet", token[:min(10, len(token))]+"...")
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

	// Ensure cleanup happens on exit
	defer func() {
		clientsMu.Lock()
		delete(clients, clientID)
		clientsMu.Unlock()
		
		// IMPORTANT: Remove from match queue if they are still there
		matchMaker.Remove(client)
		
		slog.Info("Client fully disconnected", "client_id", clientID)
	}()

	slog.Info("Client connected (Authenticated)", "client_id", clientID)

	// Send ID to client
	client.Conn.WriteJSON(Message{
		Type:    "init",
		Payload: clientID,
	})

	// Add to match queue
	matchMaker.Add(client)

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("WebSocket error", "client_id", clientID, "error", err)
			} else {
				// Normal disconnect (e.g. client closed tab)
				slog.Info("Client disconnected (ReadJSON)", "client_id", clientID)
			}
			break
		}

		msg.From = clientID
		handleMessage(msg)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
