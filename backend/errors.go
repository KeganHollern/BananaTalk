package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorResponse is the JSON body returned for all non-success HTTP responses.
// `code` is a stable machine-readable token (snake_case); `error` is the
// human-readable message.
type errorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorResponse{Code: code, Error: message}); err != nil {
		slog.Error("writeError encode failed", "error", err)
	}
}
