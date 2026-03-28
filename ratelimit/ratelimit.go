// Package ratelimit provides a simple token-bucket rate limiter for HTTP endpoints.
//
// Each client IP address gets its own bucket. The limiter supports:
//   - Configurable requests per second (rate)
//   - Configurable burst size (max tokens)
//   - Automatic cleanup of stale entries
//
// No external dependencies — uses only the Go standard library.
package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Limiter implements a per-IP token bucket rate limiter.
type Limiter struct {
	rate    float64       // tokens per second
	burst   int           // max tokens (bucket capacity)
	mu      sync.Mutex
	clients map[string]*bucket
	cleanup time.Duration // how often to clean up stale entries
}

// bucket tracks the token state for a single client.
type bucket struct {
	tokens   float64
	lastTime time.Time
}

// New creates a new rate limiter.
//   - rate:  requests per second allowed per client
//   - burst: maximum burst size (tokens accumulated while idle)
func New(rate float64, burst int) *Limiter {
	l := &Limiter{
		rate:    rate,
		burst:   burst,
		clients: make(map[string]*bucket),
		cleanup: 5 * time.Minute,
	}
	go l.cleanupLoop()
	return l
}

// Allow checks if a request from the given IP is allowed.
func (l *Limiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.clients[ip]
	if !exists {
		b = &bucket{
			tokens:   float64(l.burst) - 1, // consume one token immediately
			lastTime: time.Now(),
		}
		l.clients[ip] = b
		return true
	}

	// Refill tokens based on elapsed time.
	now := time.Now()
	elapsed := now.Sub(b.lastTime).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.lastTime = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}

	return false
}

// Middleware returns an HTTP middleware that applies rate limiting.
// Returns 429 Too Many Requests when the limit is exceeded.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if !l.Allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded, try again later"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP attempts to get the real client IP from headers or connection.
func extractIP(r *http.Request) string {
	// Check common proxy headers.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the chain.
		if idx := len(xff); idx > 0 {
			for i, c := range xff {
				if c == ',' {
					return xff[:i]
				}
			}
			return xff
		}
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return xrip
	}

	// Fall back to remote address.
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// cleanupLoop removes stale client entries periodically.
func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(l.cleanup)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		cutoff := time.Now().Add(-l.cleanup)
		for ip, b := range l.clients {
			if b.lastTime.Before(cutoff) {
				delete(l.clients, ip)
			}
		}
		l.mu.Unlock()
	}
}
