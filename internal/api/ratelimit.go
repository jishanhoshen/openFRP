package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Per-IP, per-bucket token buckets (in memory; one control-plane instance).
var bucketLimits = map[string]struct {
	every time.Duration
	burst int
}{
	"login":      {12 * time.Second, 10}, // ~5/min sustained
	"register":   {time.Minute, 5},
	"claim":      {5 * time.Second, 10}, // pairing-code guessing
	"pair-start": {10 * time.Second, 10},
	"pair-poll":  {time.Second, 20},
	// Keyed by account (lower-cased email), not IP.
	"login-account": {30 * time.Second, 10},
	// Agent config long-polls: generous, since many agents can share one NAT IP.
	"agent": {100 * time.Millisecond, 200},
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

type rateLimiter struct {
	mu sync.Mutex
	m  map[string]*limiterEntry
}

func newRateLimiter() *rateLimiter { return &rateLimiter{m: map[string]*limiterEntry{}} }

func (rl *rateLimiter) allow(bucket, ip string) bool {
	cfg, ok := bucketLimits[bucket]
	if !ok {
		return true
	}
	key := bucket + "|" + ip
	rl.mu.Lock()
	e, ok := rl.m[key]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(cfg.every), cfg.burst)}
		rl.m[key] = e
	}
	e.seen = time.Now()
	rl.mu.Unlock()
	return e.lim.Allow()
}

func (rl *rateLimiter) prune(idle time.Duration) {
	cutoff := time.Now().Add(-idle)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, e := range rl.m {
		if e.seen.Before(cutoff) {
			delete(rl.m, k)
		}
	}
}
