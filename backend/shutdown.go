package main

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// shutdownDeadline bounds the entire graceful-shutdown sequence. Sized to fit
// inside the pod's terminationGracePeriodSeconds with headroom — if this
// expires the kubelet's SIGKILL is imminent, so we stop waiting for in-flight
// handlers and let the process exit.
const shutdownDeadline = 25 * time.Second

// readinessDrainPause gives the kubelet readiness probe one or two ticks to
// observe the 503 from /readyz and pull this pod out of the Service endpoints
// before we start tearing down live sessions. Without it, a fresh client
// could land on a pod that is about to broadcast server_shutdown.
const readinessDrainPause = 3 * time.Second

// clientCloseGrace gives clients that received the server_shutdown JSON a
// moment to close their side cleanly before we force a WS close frame.
const clientCloseGrace = 1 * time.Second

// shuttingDown flips to true once a SIGTERM/SIGINT has been observed. While
// set, /readyz returns 503 (so the Service stops routing new traffic) and
// /ws upgrades are rejected. Existing sessions continue until we close them.
var shuttingDown atomic.Bool

// gracefulShutdown drains the backend in five phases:
//  1. flip shuttingDown — /readyz starts returning 503, new /ws upgrades 503.
//  2. sleep readinessDrainPause so the kubelet observes the readiness flip.
//  3. broadcast a `server_shutdown` JSON message to every connected client,
//     so the renderer can transition to a "Reconnecting…" state instead of
//     showing an error.
//  4. send a clean WS close frame and Close() each connection. The per-client
//     read loop in handleConnections will then exit and run its deferred
//     cleanup (clients map, matchmaker queue/session entries).
//  5. http.Server.Shutdown with the remaining context budget so any non-WS
//     handlers (admin, /report, /metrics) finish before the process exits.
func gracefulShutdown(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownDeadline)
	defer cancel()

	shuttingDown.Store(true)
	slog.Info("Shutdown: marked unready, waiting for traffic to shift",
		"pause", readinessDrainPause)
	time.Sleep(readinessDrainPause)

	n := broadcastServerShutdown()
	slog.Info("Shutdown: broadcast complete", "clients_notified", n)

	time.Sleep(clientCloseGrace)

	closed := closeAllClients()
	slog.Info("Shutdown: connections closed", "clients_closed", closed)

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("Shutdown: http.Server.Shutdown error", "error", err)
		return
	}
	slog.Info("Shutdown: complete")
}

// snapshotClients copies the current client list under the lock so we can
// iterate without holding clientsMu (writing a JSON message can block on a
// slow peer for up to writeWait, and we don't want to stall accept).
func snapshotClients() []*Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	out := make([]*Client, 0, len(clients))
	for _, c := range clients {
		out = append(out, c)
	}
	return out
}

func broadcastServerShutdown() int {
	list := snapshotClients()
	for _, c := range list {
		if err := c.WriteJSON(Message{Type: "server_shutdown"}); err != nil {
			slog.Debug("Shutdown: broadcast write failed",
				"client_id", c.ID, "error", err)
		}
	}
	return len(list)
}

func closeAllClients() int {
	list := snapshotClients()
	closeMsg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "server_shutdown")
	for _, c := range list {
		_ = c.WriteControl(websocket.CloseMessage, closeMsg, time.Now().Add(writeWait))
		_ = c.Conn.Close()
	}
	return len(list)
}
