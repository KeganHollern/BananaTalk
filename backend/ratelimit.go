package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// wsConnectionsPerMinute caps how many WS upgrade attempts a single client IP
// can make per minute. The burst is the same number, so a fresh IP can spike
// up to the cap before the per-minute throttle kicks in.
const wsConnectionsPerMinute = 5

// ipLimiterIdleTTL is how long an unused per-IP limiter is kept before being
// garbage collected. Keeps the map from growing unbounded.
const ipLimiterIdleTTL = 10 * time.Minute

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiter
	rate     rate.Limit
	burst    int
	trustXFF bool
}

func newIPRateLimiter(perMinute int, trustXFF bool) *ipRateLimiter {
	r := &ipRateLimiter{
		limiters: make(map[string]*ipLimiter),
		rate:     rate.Every(time.Minute / time.Duration(perMinute)),
		burst:    perMinute,
		trustXFF: trustXFF,
	}
	go r.gcLoop()
	return r
}

func (r *ipRateLimiter) get(ip string) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if l, ok := r.limiters[ip]; ok {
		l.lastSeen = now
		return l.limiter
	}
	lim := rate.NewLimiter(r.rate, r.burst)
	r.limiters[ip] = &ipLimiter{limiter: lim, lastSeen: now}
	return lim
}

func (r *ipRateLimiter) gcLoop() {
	t := time.NewTicker(ipLimiterIdleTTL)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-ipLimiterIdleTTL)
		r.mu.Lock()
		for ip, l := range r.limiters {
			if l.lastSeen.Before(cutoff) {
				delete(r.limiters, ip)
			}
		}
		r.mu.Unlock()
	}
}

func (r *ipRateLimiter) allow(req *http.Request) bool {
	ip := clientIP(req, r.trustXFF)
	if ip == "" {
		return true
	}
	return r.get(ip).Allow()
}

// clientIP returns the best-effort remote IP of the request. When trustXFF is
// true (set via TRUST_PROXY_HEADERS), the leftmost X-Forwarded-For entry takes
// precedence, then X-Real-IP, then RemoteAddr.
func clientIP(r *http.Request, trustXFF bool) string {
	if trustXFF {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
