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
)

const (
	maxScreenshotBytes = 5 << 20 // 5 MiB
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
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	// Cap the total request body up front so a hostile client can't stream a
	// huge upload past the multipart parser. The +1KiB headroom covers the
	// reason field, multipart boundaries, and other form parts.
	r.Body = http.MaxBytesReader(w, r.Body, maxScreenshotBytes+1024)

	ctx := r.Context()

	reporterSub, ok := authenticate(ctx, r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_token", "token invalid or expired")
		return
	}

	if err := r.ParseMultipartForm(maxScreenshotBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 5MB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_form", "invalid multipart form")
		return
	}

	reportedSub := strings.TrimSpace(r.FormValue("reported_user_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reportedSub == "" {
		writeError(w, http.StatusBadRequest, "missing_reported_user", "reported_user_id is required")
		return
	}
	if reportedSub == reporterSub {
		writeError(w, http.StatusBadRequest, "self_report", "cannot report yourself")
		return
	}
	if reason == "" {
		writeError(w, http.StatusBadRequest, "missing_reason", "reason is required")
		return
	}
	if len(reason) > maxReasonLen {
		reason = reason[:maxReasonLen]
	}

	file, header, err := r.FormFile("screenshot")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_screenshot", "screenshot is required")
		return
	}
	defer func() { _ = file.Close() }()

	if header.Size > maxScreenshotBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "screenshot_too_large", "screenshot exceeds 5MB limit")
		return
	}

	reporterID, _, err := upsertUser(ctx, reporterSub)
	if err != nil {
		slog.Error("report: upsert reporter", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
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
			writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
			return
		}
	}

	key, err := screenshotKey()
	if err != nil {
		slog.Error("report: generate key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}

	url, err := storageClient.Upload(ctx, key, "image/png", file, header.Size)
	if err != nil {
		slog.Error("report: upload screenshot", "error", err)
		writeError(w, http.StatusBadGateway, "upload_failed", "screenshot upload failed")
		return
	}

	banned, err := recordReport(ctx, reporterID, reportedID, reason, url, key)
	if err != nil {
		slog.Error("report: persist", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}

	// recordReport persisted a symmetric pair of block rows inside its txn;
	// reflect both directions in the online block SETs so the matchmaker
	// honors the block before either side reconnects. AddBlock is a no-op for
	// offline users, who'll rehydrate from Postgres on next connect.
	matchMaker.AddBlock(ctx, reporterSub, reportedSub)
	matchMaker.AddBlock(ctx, reportedSub, reporterSub)

	if banned {
		slog.Info("Auto-banned user", "reported_sub", reportedSub, "reported_id", reportedID)
		// Sever any active WebSocket the banned user has open.
		clientsMu.Lock()
		if c, ok := clients[reportedSub]; ok {
			_ = c.Conn.Close()
		}
		clientsMu.Unlock()
	}

	bannedLabel := "false"
	if banned {
		bannedLabel = "true"
	}
	reportsTotal.WithLabelValues(bannedLabel).Inc()

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
	token := bearerToken(r)
	sub, code, verr := verifyToken(ctx, token)
	if code != "" {
		logTokenFailure(code, verr, token, r.RemoteAddr)
		return "", false
	}
	return sub, true
}

func screenshotKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "reports/" + hex.EncodeToString(b[:]) + ".png", nil
}
