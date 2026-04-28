package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPRateLimiter_AdmitsThenDenies(t *testing.T) {
	// burst==per-minute, so a fresh IP can spike to wsConnectionsPerMinute
	// before the throttle kicks in.
	rl := newIPRateLimiter(wsConnectionsPerMinute, false)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	for i := range wsConnectionsPerMinute {
		if !rl.allow(req) {
			t.Fatalf("attempt %d: want allow, got deny", i+1)
		}
	}
	if rl.allow(req) {
		t.Fatalf("attempt %d: expected deny once burst is exhausted", wsConnectionsPerMinute+1)
	}
}

func TestIPRateLimiter_PerIPIsolation(t *testing.T) {
	rl := newIPRateLimiter(wsConnectionsPerMinute, false)
	a := httptest.NewRequest(http.MethodGet, "/ws", nil)
	a.RemoteAddr = "10.0.0.1:1111"
	b := httptest.NewRequest(http.MethodGet, "/ws", nil)
	b.RemoteAddr = "10.0.0.2:2222"

	for range wsConnectionsPerMinute {
		_ = rl.allow(a)
	}
	if rl.allow(a) {
		t.Fatalf("ip A: expected deny after burst")
	}
	if !rl.allow(b) {
		t.Fatalf("ip B: expected admit (separate bucket)")
	}
}

func TestClientIP_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		trustXFF   bool
		remoteAddr string
		xff        string
		xri        string
		want       string
	}{
		{
			name:       "trust off ignores XFF",
			trustXFF:   false,
			remoteAddr: "10.0.0.1:1234",
			xff:        "1.2.3.4",
			xri:        "5.6.7.8",
			want:       "10.0.0.1",
		},
		{
			name:       "trust on prefers leftmost XFF",
			trustXFF:   true,
			remoteAddr: "10.0.0.1:1234",
			xff:        "1.2.3.4, 9.9.9.9",
			xri:        "5.6.7.8",
			want:       "1.2.3.4",
		},
		{
			name:       "trust on falls back to X-Real-IP when XFF missing",
			trustXFF:   true,
			remoteAddr: "10.0.0.1:1234",
			xff:        "",
			xri:        "5.6.7.8",
			want:       "5.6.7.8",
		},
		{
			name:       "trust on falls back to RemoteAddr when both missing",
			trustXFF:   true,
			remoteAddr: "10.0.0.1:1234",
			want:       "10.0.0.1",
		},
		{
			name:       "trust on with empty leftmost XFF entry skips to X-Real-IP",
			trustXFF:   true,
			remoteAddr: "10.0.0.1:1234",
			xff:        " , 9.9.9.9",
			xri:        "5.6.7.8",
			want:       "5.6.7.8",
		},
		{
			name:       "RemoteAddr without port returns as-is",
			trustXFF:   false,
			remoteAddr: "10.0.0.1",
			want:       "10.0.0.1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xri != "" {
				req.Header.Set("X-Real-IP", tc.xri)
			}
			if got := clientIP(req, tc.trustXFF); got != tc.want {
				t.Fatalf("clientIP: want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestIPRateLimiter_AllowEmptyIPPasses(t *testing.T) {
	// If clientIP returns empty (degenerate request), allow defaults to true
	// rather than dropping the request.
	rl := newIPRateLimiter(wsConnectionsPerMinute, true)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.RemoteAddr = ""
	if !rl.allow(req) {
		t.Fatal("empty ip: expected allow=true")
	}
}
