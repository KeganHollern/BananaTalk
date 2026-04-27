package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"google.golang.org/api/idtoken"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10
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
	mu   sync.Mutex
}

func (c *Client) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.Conn.WriteJSON(v)
}

func (c *Client) WriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteControl(messageType, data, deadline)
}

var (
	clients    = make(map[string]*Client)
	clientsMu  sync.Mutex
	rdb        *redis.Client
	matchMaker *MatchMaker
	wsLimiter  *ipRateLimiter
)

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	redisDB := 0
	if dbStr := os.Getenv("REDIS_DB"); dbStr != "" {
		if db, err := strconv.Atoi(dbStr); err == nil {
			redisDB = db
		}
	}

	rdb = redis.NewClient(&redis.Options{
		Addr:     getEnv("REDIS_ADDR", "localhost:6379"),
		Password: getEnv("REDIS_PASSWORD", ""),
		DB:       redisDB,
	})

	ctx := context.Background()
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		slog.Error("Redis connection failed", "addr", getEnv("REDIS_ADDR", "localhost:6379"), "error", err)
		os.Exit(1)
	}
	slog.Info("Connected to Redis", "addr", getEnv("REDIS_ADDR", "localhost:6379"))

	matchMaker = NewMatchMaker(rdb)

	trustProxy := strings.EqualFold(getEnv("TRUST_PROXY_HEADERS", ""), "true")
	wsLimiter = newIPRateLimiter(wsConnectionsPerMinute, trustProxy)

	dbDSN := getEnv("DB_DSN", "")
	if dbDSN == "" {
		slog.Error("DB_DSN environment variable is required")
		os.Exit(1)
	}
	var dbErr error
	db, dbErr = initDB(ctx, dbDSN)
	if dbErr != nil {
		slog.Error("PostgreSQL connection failed", "error", dbErr)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("Connected to PostgreSQL")

	storageClient, dbErr = newStorage(ctx)
	if dbErr != nil {
		slog.Error("Object storage init failed", "error", dbErr)
		os.Exit(1)
	}
	slog.Info("Object storage ready", "provider", getEnv("STORAGE_PROVIDER", ""), "bucket", getEnv("STORAGE_BUCKET", ""))

	http.HandleFunc("/ws", handleConnections)
	http.HandleFunc("/report", reportHandler)
	http.Handle("/metrics", metricsHandler())
	if initAdmin() {
		slog.Info("Admin dashboard mounted at /admin/")
	}
	http.HandleFunc("/", handleNotFound)

	port := ":8080"
	go matchMaker.Run(ctx)

	slog.Info("BananaTalk Backend starting", "port", port)
	err := http.ListenAndServe(port, nil)
	if err != nil {
		slog.Error("ListenAndServe failed", "error", err)
		os.Exit(1)
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	// 0. Per-IP rate limit on upgrade attempts. Applied before auth so
	// unauthenticated floods do not exercise the token verifier.
	if !wsLimiter.allow(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many connection attempts, retry shortly")
		slog.Warn("WS upgrade rate-limited", "remote_addr", r.RemoteAddr)
		return
	}

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
		writeError(w, http.StatusUnauthorized, "missing_token", "authentication token required")
		slog.Warn("Connection attempt without token", "remote_addr", r.RemoteAddr)
		return
	}

	// 2. Verify Token
	ctx := context.Background()
	// validating for any audience for now, as we might have multiple client IDs (iOS, Web, Android)
	// Passing empty string as audience skips audience check, which we can refine later if needed.
	payload, err := idtoken.Validate(ctx, token, "")
	if err != nil {
		slog.Info("JWT validation failed", "error", err, "remote_addr", r.RemoteAddr, "token_snippet", token[:min(10, len(token))]+"...")
		writeError(w, http.StatusUnauthorized, "invalid_token", "token invalid or expired")
		return
	}

	// Defense-in-depth: idtoken.Validate already checks `exp`, but make the
	// expiration policy explicit at the boundary so clock-skew tweaks cannot
	// silently relax it.
	if payload.Expires <= time.Now().Unix() {
		slog.Info("JWT expired at connect", "remote_addr", r.RemoteAddr, "exp", payload.Expires)
		writeError(w, http.StatusUnauthorized, "token_expired", "token expired")
		return
	}

	// 3. Extract Unique User ID (sub)
	userID := payload.Subject
	if userID == "" {
		slog.Error("Token payload missing subject", "remote_addr", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid_token_claims", "token missing subject")
		return
	}

	// 4. Persist user on first login
	if _, _, err := upsertUser(ctx, userID); err != nil {
		slog.Error("Failed to upsert user", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	// 5. Reject banned users.
	banned, err := isUserBanned(ctx, userID)
	if err != nil {
		slog.Error("Failed to check ban status", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if banned {
		slog.Info("Banned user denied connection", "user_id", userID)
		writeError(w, http.StatusForbidden, "account_suspended", "account suspended")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	clientID := userID
	client := &Client{ID: clientID, Conn: conn}

	clientsMu.Lock()
	clients[clientID] = client
	clientsMu.Unlock()
	activeConnections.Inc()

	// Ensure cleanup happens on exit
	defer func() {
		clientsMu.Lock()
		delete(clients, clientID)
		clientsMu.Unlock()
		activeConnections.Dec()

		// Remove from match queue if still waiting
		matchMaker.Remove(ctx, clientID)
		// Clear any active session mapping
		matchMaker.DeleteSession(ctx, clientID)

		slog.Info("Client fully disconnected", "client_id", clientID)
	}()

	slog.Info("Client connected (Authenticated)", "client_id", clientID)

	// Send ID to client
	if err := client.WriteJSON(Message{
		Type:    "init",
		Payload: clientID,
	}); err != nil {
		slog.Error("Failed to send init message", "client_id", clientID, "error", err)
		return
	}

	// Subscribe to per-client match notification channel before enqueuing so
	// we never miss a notification published by any backend instance.
	notifySub := rdb.Subscribe(ctx, redisNotifyPfx+clientID)
	defer func() { _ = notifySub.Close() }()

	go func() {
		for msg := range notifySub.Channel() {
			peerID := msg.Payload
			slog.Info("Client matched via Redis notify", "client_id", clientID, "peer_id", peerID)
			if err := client.WriteJSON(Message{
				Type:    "match",
				Payload: peerID,
			}); err != nil {
				slog.Error("Failed to send match message", "client_id", clientID, "error", err)
			}
		}
	}()

	// Add to match queue
	matchMaker.Add(ctx, clientID)

	// Start heartbeat
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for range ticker.C {
			if err := client.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				slog.Info("Ping failed, closing connection", "client_id", clientID, "error", err)
				return
			}
		}
	}()

	// Limit message size to 8KB (enough for SDP)
	conn.SetReadLimit(8192)
	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		slog.Error("Failed to set read deadline", "client_id", clientID, "error", err)
		return
	}
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
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
		err := target.WriteJSON(msg)
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
