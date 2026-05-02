package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
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
	http.HandleFunc("/block", blockHandler)
	http.HandleFunc("/healthz", livenessHandler)
	http.HandleFunc("/readyz", readinessHandler)
	http.Handle("/metrics", metricsHandler())
	if initAdmin() {
		slog.Info("Admin dashboard mounted at /admin/")
	}
	http.HandleFunc("/", handleNotFound)

	port := ":8080"
	go matchMaker.Run(ctx)

	server := &http.Server{Addr: port}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("BananaTalk Backend starting", "port", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-serverErr:
		if err != nil {
			slog.Error("ListenAndServe failed", "error", err)
			os.Exit(1)
		}
	case sig := <-sigCh:
		slog.Info("Shutdown signal received", "signal", sig.String())
		gracefulShutdown(server)
	}
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	// Reject new upgrades on a draining pod. The Service will pull this pod
	// from its endpoints once the readiness probe trips, but until that
	// happens (and for any client that already had this pod selected) we
	// 503 so the client retries against a healthy replica.
	if shuttingDown.Load() {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "server is shutting down")
		return
	}

	// 0. Per-IP rate limit on upgrade attempts. Applied before auth so
	// unauthenticated floods do not exercise the token verifier.
	if !wsLimiter.allow(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many connection attempts, retry shortly")
		slog.Warn("WS upgrade rate-limited", "remote_addr", r.RemoteAddr)
		return
	}

	// 1. Extract + verify token. verifyToken handles missing / invalid /
	// expired / missing-subject in one place (see auth.go).
	token := bearerToken(r)
	ctx := context.Background()
	userID, code, verr := verifyToken(ctx, token)
	if code != "" {
		logTokenFailure(code, verr, token, r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, code, tokenErrMessage(code))
		return
	}

	// 4. Persist user on first login
	internalID, _, err := upsertUser(ctx, userID)
	if err != nil {
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

	// Hydrate the user's block SET in Redis before they can be matched.
	// loadUserBlocks failure is logged but not fatal — the matchmaker would
	// still pair them with people they've blocked, but that's a degraded
	// behavior, not a correctness violation.
	if subs, err := loadUserBlocks(ctx, internalID); err != nil {
		slog.Error("Failed to load user blocks", "user_id", userID, "error", err)
	} else {
		matchMaker.HydrateBlocks(ctx, userID, subs)
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
		// Drop the cached block SET; a future connect re-hydrates from DB.
		matchMaker.ClearBlocks(ctx, clientID)

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

func handleMessage(msg Message) {
	// Control messages handled server-side, never relayed to a peer.
	if msg.Type == "connect_metrics" {
		recordConnectMetrics(msg)
		return
	}

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

// recordConnectMetrics observes a client-reported connection-timing payload
// into Prometheus histograms. Payload schema (all *_ms fields are integers
// measured from the moment the client received the `match` message):
//
//	{
//	  "role": "offerer" | "answerer",
//	  "queue_wait_ms": 1234,
//	  "first_track_ms": 800,
//	  "first_frame_ms": 1100,
//	  "ice_gathering_complete_ms": 950,
//	  "offer_sent_ms": 60,         // offerer only
//	  "answer_received_ms": 700    // offerer only
//	}
//
// Unknown / non-numeric fields are ignored so the format can evolve without
// breaking older clients.
func recordConnectMetrics(msg Message) {
	payload, ok := msg.Payload.(map[string]interface{})
	if !ok {
		return
	}
	role, _ := payload["role"].(string)
	if role != "offerer" && role != "answerer" {
		role = "unknown"
	}

	observePhase := func(phase, key string) {
		v, ok := numberField(payload[key])
		if !ok {
			return
		}
		connectTimeSeconds.WithLabelValues(phase, role).Observe(v / 1000.0)
	}
	observePhase("total", "first_frame_ms")
	observePhase("first_track", "first_track_ms")
	observePhase("ice_gathering", "ice_gathering_complete_ms")
	observePhase("sdp_offer", "offer_sent_ms")
	observePhase("sdp_answer", "answer_received_ms")

	if v, ok := numberField(payload["queue_wait_ms"]); ok {
		queueWaitSeconds.Observe(v / 1000.0)
	}

	slog.Info("connect_metrics",
		"client_id", msg.From,
		"role", role,
		"first_frame_ms", payload["first_frame_ms"],
		"first_track_ms", payload["first_track_ms"],
		"ice_gathering_complete_ms", payload["ice_gathering_complete_ms"],
		"offer_sent_ms", payload["offer_sent_ms"],
		"answer_received_ms", payload["answer_received_ms"],
		"queue_wait_ms", payload["queue_wait_ms"],
	)
}

func numberField(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		slog.Info("Redirecting 404", "path", r.URL.Path, "remote_addr", r.RemoteAddr)
	}
	http.Redirect(w, r, "https://lystic.dev", http.StatusFound)
}
