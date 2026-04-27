package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed admin_ui
var adminUIFS embed.FS

const (
	adminSignedURLTTL = 15 * time.Minute
	adminDefaultLimit = 20
	adminMaxLimit     = 100
)

var (
	adminUser string
	adminPass string
)

// initAdmin reads ADMIN_USERNAME / ADMIN_PASSWORD from the environment. The
// admin endpoints are only registered if both are set; this lets a deployment
// opt out of exposing the dashboard entirely.
func initAdmin() bool {
	adminUser = getEnv("ADMIN_USERNAME", "")
	adminPass = getEnv("ADMIN_PASSWORD", "")
	if adminUser == "" || adminPass == "" {
		slog.Warn("Admin dashboard disabled: ADMIN_USERNAME / ADMIN_PASSWORD not set")
		return false
	}

	uiSub, err := fs.Sub(adminUIFS, "admin_ui")
	if err != nil {
		slog.Error("admin: embed sub", "error", err)
		return false
	}
	uiHandler := http.StripPrefix("/admin/", http.FileServer(http.FS(uiSub)))

	http.HandleFunc("/admin/api/reports", adminAuth(adminListReports))
	http.HandleFunc("/admin/api/reports/", adminAuth(adminGetReport))
	http.HandleFunc("/admin/api/users/", adminAuth(adminUserAction))
	http.HandleFunc("/admin/", adminAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/admin/index.html"
			uiHandler.ServeHTTP(w, r2)
			return
		}
		uiHandler.ServeHTTP(w, r)
	}))

	return true
}

// adminAuth wraps a handler with HTTP Basic Auth backed by the env-var
// credentials. Uses constant-time comparison.
func adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(adminUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(adminPass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="BananaTalk Admin", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("admin: write json", "error", err)
	}
}

// GET /admin/api/reports?reason=&page=1&limit=20
func adminListReports(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	reason := strings.TrimSpace(q.Get("reason"))
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = adminDefaultLimit
	}
	if limit > adminMaxLimit {
		limit = adminMaxLimit
	}
	offset := (page - 1) * limit

	rows, total, err := listReports(r.Context(), reason, limit, offset)
	if err != nil {
		slog.Error("admin: list reports", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type item struct {
		ReportRow
		ThumbnailURL string `json:"thumbnail_url"`
	}
	items := make([]item, 0, len(rows))
	for _, row := range rows {
		thumb := signScreenshot(r.Context(), row)
		items = append(items, item{ReportRow: row, ThumbnailURL: thumb})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": (total + limit - 1) / limit,
	})
}

// GET /admin/api/reports/{id}
func adminGetReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/admin/api/reports/")
	if idStr == "" || strings.Contains(idStr, "/") {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	row, err := getReport(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		slog.Error("admin: get report", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	signed := signScreenshot(r.Context(), row)
	writeJSON(w, http.StatusOK, map[string]any{
		"report":               row,
		"signed_screenshot_url": signed,
	})
}

// POST /admin/api/users/{id}/ban    (or .../unban)
func adminUserAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/api/users/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch parts[1] {
	case "ban":
		sub, changed, err := banUser(r.Context(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			slog.Error("admin: ban", "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if changed {
			disconnectClient(sub)
			slog.Info("Admin banned user", "user_id", id, "google_sub", sub)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      id,
			"banned":  true,
			"changed": changed,
		})
	case "unban":
		sub, changed, err := unbanUser(r.Context(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			slog.Error("admin: unban", "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if changed {
			slog.Info("Admin unbanned user", "user_id", id, "google_sub", sub)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      id,
			"banned":  false,
			"changed": changed,
		})
	default:
		http.NotFound(w, r)
	}
}

// signScreenshot returns a URL the dashboard can fetch for the report's
// screenshot. Falls back to the stored URL if signing fails (dev/local file
// storage, etc.).
func signScreenshot(ctx context.Context, row ReportRow) string {
	if row.ScreenshotKey == "" {
		return row.ScreenshotURL
	}
	url, err := storageClient.Sign(ctx, row.ScreenshotKey, adminSignedURLTTL)
	if err != nil {
		slog.Warn("admin: sign screenshot", "key", row.ScreenshotKey, "error", err)
		return row.ScreenshotURL
	}
	return url
}

// disconnectClient closes the websocket of the given user if one is open. It
// mirrors the auto-ban flow in reportHandler.
func disconnectClient(googleSub string) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[googleSub]; ok {
		_ = c.Conn.Close()
	}
}
