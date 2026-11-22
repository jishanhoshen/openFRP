package stats

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tunnelhub/internal/store"
)

type memSink struct {
	mu       sync.Mutex
	counters map[string][2]int64
	totals   map[string][2]int64 // "<dev>.<name>" -> accumulated in/out
}

func (m *memSink) LoadTrafficCounters(context.Context) (map[string][2]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][2]int64{}
	for k, v := range m.counters {
		out[k] = v
	}
	return out, nil
}

func (m *memSink) AddTraffic(_ context.Context, ds []store.TrafficDelta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range ds {
		t := m.totals[d.DeviceID+"."+d.Name]
		m.totals[d.DeviceID+"."+d.Name] = [2]int64{t[0] + d.In, t[1] + d.Out}
		m.counters[d.ProxyName] = [2]int64{d.CurIn, d.CurOut}
	}
	return nil
}

type fakeFRPS struct {
	mu  sync.Mutex
	tcp []proxyStats
}

func (f *fakeFRPS) set(in, out int64, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tcp = []proxyStats{
		{Name: "dev-abc.mc", TodayTrafficIn: in, TodayTrafficOut: out, CurConns: 2, Status: status},
		{Name: "someone-else.proxy", TodayTrafficIn: 999}, // not ours: ignored
	}
}

func (f *fakeFRPS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "pw" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var list []proxyStats
	if r.URL.Path == "/api/proxy/tcp" {
		list = f.tcp
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"proxies": list})
}

func newCollector(t *testing.T, url string, sink *memSink) *Collector {
	return &Collector{URL: url, User: "admin", Password: "pw", Interval: time.Minute, Sink: sink,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestCollectorDeltasAndResets(t *testing.T) {
	f := &fakeFRPS{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	sink := &memSink{counters: map[string][2]int64{}, totals: map[string][2]int64{}}
	c := newCollector(t, srv.URL, sink)
	ctx := context.Background()

	steps := []struct {
		in, out      int64
		wantIn, wOut int64
	}{
		{100, 1000, 100, 1000}, // first sample: counted from zero
		{150, 1500, 150, 1500}, // +50 / +500
		{150, 1500, 150, 1500}, // unchanged
		{20, 30, 170, 1530},    // reset (midnight / frps restart): new value is the delta
	}
	for i, s := range steps {
		f.set(s.in, s.out, "online")
		if err := c.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		got := sink.totals["dev-abc.mc"]
		if got != [2]int64{s.wantIn, s.wOut} {
			t.Fatalf("step %d: totals %v, want [%d %d]", i, got, s.wantIn, s.wOut)
		}
	}
	if _, ok := sink.totals["someone-else.proxy"]; ok {
		t.Fatal("foreign proxy should be ignored")
	}
	l, ok := c.Live("dev-abc", "mc")
	if !ok || !l.Online || l.Connections != 2 || l.TodayIn != 20 {
		t.Fatalf("live = %+v ok=%v", l, ok)
	}

	// A restarted control plane resumes from stored counters (no double count).
	c2 := newCollector(t, srv.URL, sink)
	f.set(25, 40, "offline")
	if err := c2.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sink.totals["dev-abc.mc"]; got != [2]int64{175, 1540} {
		t.Fatalf("after restart totals %v", got)
	}
	if l, _ := c2.Live("dev-abc", "mc"); l.Online {
		t.Fatal("offline proxy reported online")
	}
}

func TestCollectorAuthFailure(t *testing.T) {
	srv := httptest.NewServer(&fakeFRPS{})
	defer srv.Close()
	sink := &memSink{counters: map[string][2]int64{}, totals: map[string][2]int64{}}
	c := newCollector(t, srv.URL, sink)
	c.Password = "wrong"
	if err := c.Poll(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := c.Live("dev-abc", "mc"); ok {
		t.Fatal("no live data expected")
	}
}
