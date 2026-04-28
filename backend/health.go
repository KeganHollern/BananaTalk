package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// healthProbeTimeout bounds each dependency ping in /readyz. Kept short so a
// stuck dependency cannot keep a probe in flight long enough to overlap the
// next periodSeconds tick from the kubelet.
const healthProbeTimeout = 1500 * time.Millisecond

// livenessHandler answers /healthz. It only attests that the HTTP server
// goroutine is up — no dependency checks. A transient Redis or Postgres blip
// must not restart a pod that is otherwise serving live WebSocket calls.
func livenessHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// readinessHandler answers /readyz. Pings Redis and Postgres with a short
// per-check deadline. Failure pulls the pod out of the Service endpoints
// (kubelet stops sending new traffic) without killing in-flight WS sessions.
//
// Once shuttingDown is set, returns 503 unconditionally so the kubelet pulls
// this pod from the Service before gracefulShutdown closes existing sessions.
func readinessHandler(w http.ResponseWriter, r *http.Request) {
	if shuttingDown.Load() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Warn("readyz: redis ping failed", "error", err)
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}

	if err := db.Ping(ctx); err != nil {
		slog.Warn("readyz: postgres ping failed", "error", err)
		http.Error(w, "postgres unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}
