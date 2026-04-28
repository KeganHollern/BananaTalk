package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

// Stable codes returned by verifyToken so renderers and tests can match on
// them without depending on slog wording.
const (
	tokenErrMissing = "missing_token"
	tokenErrInvalid = "invalid_token"
	tokenErrExpired = "token_expired"
	tokenErrNoSub   = "invalid_token_claims"
)

// tokenValidate is a package-level indirection so tests can stub the verifier
// without contacting Google's keyserver. Defaults to idtoken.Validate.
var tokenValidate = func(ctx context.Context, token, audience string) (*idtoken.Payload, error) {
	return idtoken.Validate(ctx, token, audience)
}

// bearerToken returns the request's bearer credential, preferring the `token`
// query string (used by WS upgrades that cannot send headers) and falling
// back to the Authorization header.
func bearerToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// verifyToken validates a Google ID token and returns the subject claim. On
// failure it returns one of the tokenErr* codes plus the underlying error
// when available (used only for logging — the code is what callers branch on).
func verifyToken(ctx context.Context, token string) (sub string, code string, err error) {
	if token == "" {
		return "", tokenErrMissing, nil
	}
	payload, err := tokenValidate(ctx, token, "")
	if err != nil {
		return "", tokenErrInvalid, err
	}
	// Defense-in-depth: idtoken.Validate already enforces exp, but pin the
	// policy at our boundary so future library tweaks cannot silently relax it.
	if payload.Expires <= time.Now().Unix() {
		return "", tokenErrExpired, nil
	}
	if payload.Subject == "" {
		return "", tokenErrNoSub, nil
	}
	return payload.Subject, "", nil
}

// tokenErrMessage maps a tokenErr* code to the user-facing message returned
// in the JSON error body.
func tokenErrMessage(code string) string {
	switch code {
	case tokenErrMissing:
		return "authentication token required"
	case tokenErrInvalid:
		return "token invalid or expired"
	case tokenErrExpired:
		return "token expired"
	case tokenErrNoSub:
		return "token missing subject"
	default:
		return "unauthorized"
	}
}

// logTokenFailure mirrors the per-code logging the WS handler used to emit
// inline. Kept here so report.go and any future caller log the same context.
func logTokenFailure(code string, verr error, token, remoteAddr string) {
	switch code {
	case tokenErrMissing:
		slog.Warn("Connection attempt without token", "remote_addr", remoteAddr)
	case tokenErrInvalid:
		snippet := token
		if len(snippet) > 10 {
			snippet = snippet[:10]
		}
		slog.Info("JWT validation failed", "error", verr, "remote_addr", remoteAddr, "token_snippet", snippet+"...")
	case tokenErrExpired:
		slog.Info("JWT expired at connect", "remote_addr", remoteAddr)
	case tokenErrNoSub:
		slog.Error("Token payload missing subject", "remote_addr", remoteAddr)
	}
}
