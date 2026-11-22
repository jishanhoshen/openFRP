// Package stats polls the frps dashboard API for per-tunnel status and
// traffic, keeps the live view in memory and persists daily traffic totals.
//
// frps only exposes "today's" byte counters per proxy (in memory, reset at
// frps' local midnight and on frps restarts). The collector turns them into
// deltas: if a counter went down it was reset, and the new value is the delta.
// Totals are therefore approximate around resets (bytes between the last poll
// and a reset are lost), which is fine for usage statistics.
package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"tunnelhub/internal/store"
)

// Sink persists traffic (implemented by store.Store).
type Sink interface {
	LoadTrafficCounters(ctx context.Context) (map[string][2]int64, error)
	AddTraffic(ctx context.Context, ds []store.TrafficDelta) error
}

// Live is the current state of one tunnel as seen by the relay.
type Live struct {
	Online      bool      `json:"online"`
	Connections int64     `json:"connections"`
	TodayIn     int64     `json:"today_bytes_in"`
	TodayOut    int64     `json:"today_bytes_out"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Collector struct {
	URL      string // frps webServer base URL, e.g. http://10.250.0.1:7500
	User     string
	Password string
	Interval time.Duration
	Sink     Sink
	Log      *slog.Logger
	Client   *http.Client

	mu       sync.RWMutex
	live     map[string]Live // key: "<device>.<name>"
	last     map[string][2]int64
	lastOK   time.Time
	failures int64
	loaded   bool
}

type proxyStats struct {
	Name            string `json:"name"`
	TodayTrafficIn  int64  `json:"todayTrafficIn"`
	TodayTrafficOut int64  `json:"todayTrafficOut"`
	CurConns        int64  `json:"curConns"`
	Status          string `json:"status"`
}

// Run polls until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		if err := c.Poll(ctx); err != nil && ctx.Err() == nil {
			c.mu.Lock()
			c.failures++
			c.mu.Unlock()
			c.Log.Warn("stats poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Live returns a tunnel's live state; ok is false if unknown or stale.
func (c *Collector) Live(deviceID, name string) (Live, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Since(c.lastOK) > 3*c.Interval+5*time.Second {
		return Live{}, false
	}
	l, ok := c.live[deviceID+"."+name]
	return l, ok
}

// Healthy reports whether the last poll succeeded recently, and the number of
// failed polls so far (exported as a metric).
func (c *Collector) Healthy() (bool, int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.lastOK) <= 3*c.Interval+5*time.Second, c.failures
}

// Poll fetches tcp and udp proxy stats once.
func (c *Collector) Poll(ctx context.Context) error {
	c.mu.Lock()
	if !c.loaded {
		last, err := c.Sink.LoadTrafficCounters(ctx)
		if err != nil {
			c.mu.Unlock()
			return fmt.Errorf("load counters: %w", err)
		}
		c.last, c.loaded = last, true
	}
	c.mu.Unlock()

	var all []proxyStats
	for _, typ := range []string{"tcp", "udp"} {
		ps, err := c.fetch(ctx, typ)
		if err != nil {
			return err
		}
		all = append(all, ps...)
	}

	now := time.Now()
	live := make(map[string]Live, len(all))
	var deltas []store.TrafficDelta
	c.mu.RLock()
	for _, p := range all {
		dev, name, ok := strings.Cut(p.Name, ".")
		if !ok || !strings.HasPrefix(dev, "dev-") {
			continue
		}
		live[p.Name] = Live{Online: p.Status == "online", Connections: p.CurConns,
			TodayIn: p.TodayTrafficIn, TodayOut: p.TodayTrafficOut, UpdatedAt: now}
		prev, seen := c.last[p.Name]
		if seen && prev[0] == p.TodayTrafficIn && prev[1] == p.TodayTrafficOut {
			continue // nothing new
		}
		deltas = append(deltas, store.TrafficDelta{
			ProxyName: p.Name, DeviceID: dev, Name: name,
			In: delta(prev[0], p.TodayTrafficIn), Out: delta(prev[1], p.TodayTrafficOut),
			CurIn: p.TodayTrafficIn, CurOut: p.TodayTrafficOut,
		})
	}
	c.mu.RUnlock()

	if err := c.Sink.AddTraffic(ctx, deltas); err != nil {
		return fmt.Errorf("store traffic: %w", err)
	}
	c.mu.Lock()
	for _, d := range deltas {
		c.last[d.ProxyName] = [2]int64{d.CurIn, d.CurOut}
	}
	c.live, c.lastOK = live, now
	c.mu.Unlock()
	return nil
}

// delta returns the growth of a "today" counter, treating a decrease as a reset.
func delta(prev, cur int64) int64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}

func (c *Collector) fetch(ctx context.Context, typ string) ([]proxyStats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/proxy/"+typ, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.User, c.Password)
	hc := c.Client
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("frps api %s: HTTP %d", typ, resp.StatusCode)
	}
	var body struct {
		Proxies []proxyStats `json:"proxies"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("frps api %s: %w", typ, err)
	}
	return body.Proxies, nil
}
