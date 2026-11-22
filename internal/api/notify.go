package api

import (
	"net/http"
	"sync"
)

// notifier wakes long-polling agents when their configuration changes.
// Each device has a channel that is closed (and replaced) on every change.
type notifier struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func newNotifier() *notifier { return &notifier{m: map[string]chan struct{}{}} }

// wait returns a channel that closes on the next change for the device.
func (n *notifier) wait(deviceID string) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, ok := n.m[deviceID]
	if !ok {
		ch = make(chan struct{})
		n.m[deviceID] = ch
	}
	return ch
}

func (n *notifier) notify(deviceID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ch, ok := n.m[deviceID]; ok {
		close(ch)
		delete(n.m, deviceID)
	}
}

// notifyUserDevices wakes every device owned by the user (after a user is
// disabled or re-enabled, or their quota changes).
func (s *Server) notifyUserDevices(r *http.Request, userID int64) {
	ds, err := s.store.ListDevices(r.Context(), &userID)
	if err != nil {
		s.log.Warn("notify user devices: list failed", "err", err)
		return
	}
	for _, d := range ds {
		s.notify.notify(d.ID)
	}
}
