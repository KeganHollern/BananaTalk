package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/api/idtoken"
)

const (
	maxScreenshotBytes = 10 << 20 // 10 MiB
	maxReasonLen       = 500
)

var storageClient Storage

// reportHandler accepts multipart form uploads of a screenshot plus the
// reported user's google_sub and a reason string. The screenshot is stored
// in object storage and a row is persisted in the reports table. If the
// reported user crosses the 24-hour threshold they are auto-banned.
func reportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	reporterSub, ok := authenticate(ctx, r)
	if !ok {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	if err := r.ParseMultipartForm(maxScreenshotBytes); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	reportedSub := strings.TrimSpace(r.FormValue("reported_user_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reportedSub == "" {
		http.Error(w, "reported_user_id is required", http.StatusBadRequest)
		return
	}
	if reportedSub == reporterSub {
		http.Error(w, "cannot report yourself", http.StatusBadRequest)
		return
	}
	if reason == "" {
		http.Error(w, "reason is required", http.StatusBadRequest)
		return
	}
	if len(reason) > maxReasonLen {
		reason = reason[:maxReasonLen]
	}

	file, header, err := r.FormFile("screenshot")
	if err != nil {
		http.Error(w, "screenshot is required", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	if header.Size > maxScreenshotBytes {
		http.Error(w, "screenshot too large", http.StatusRequestEntityTooLarge)
		return
	}

	reporterID, _, err := upsertUser(ctx, reporterSub)
	if err != nil {
		slog.Error("report: upsert reporter", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	reportedID, err := getUserIDByGoogleSub(ctx, reportedSub)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Reported user has never connected; create a stub record so we
			// can still track reports against them.
			reportedID, _, err = upsertUser(ctx, reportedSub)
		}
		if err != nil {
			slog.Error("report: lookup reported", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	key, err := screenshotKey()
	if err != nil {
		slog.Error("report: generate key", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	url, err := storageClient.Upload(ctx, key, "image/png", file, header.Size)
	if err != nil {
		slog.Error("report: upload screenshot", "error", err)
		http.Error(w, "upload failed", http.StatusBadGateway)
		return
	}

	banned, err := recordReport(ctx, reporterID, reportedID, reason, url, key)
	if err != nil {
		slog.Error("report: persist", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if banned {
		slog.Info("Auto-banned user", "reported_sub", reportedSub, "reported_id", reportedID)
		// Sever any active WebSocket the banned user has open.
		clientsMu.Lock()
		if c, ok := clients[reportedSub]; ok {
			_ = c.Conn.Close()
		}
		clientsMu.Unlock()
	}

	slog.Info("Report received",
		"reporter_sub", reporterSub,
		"reported_sub", reportedSub,
		"banned", banned,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `{"banned":%t}`, banned)
}

// authenticate extracts and validates the bearer token from the request.
// Returns the google_sub of the authenticated user.
func authenticate(ctx context.Context, r *http.Request) (string, bool) {
	token := r.URL.Query().Get("token")
	if token == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}
	if token == "" {
		return "", false
	}

	payload, err := idtoken.Validate(ctx, token, "")
	if err != nil {
		slog.Info("token validation failed", "error", err, "remote_addr", r.RemoteAddr)
		return "", false
	}
	if payload.Subject == "" {
		return "", false
	}
	return payload.Subject, true
}

func screenshotKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "reports/" + hex.EncodeToString(b[:]) + ".png", nil
}
