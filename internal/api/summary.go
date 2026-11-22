package api

import (
	"net/http"

	"tunnelhub/internal/store"
)

// summary is the data behind the Overview page: counts for the stat cards and
// a per-day traffic series for the chart.
type summary struct {
	Computers       int                `json:"computers"`
	ComputersOnline int                `json:"computers_online"`
	Tunnels         int                `json:"tunnels"`
	TunnelsLive     int                `json:"tunnels_live"`
	Traffic30Days   []store.TrafficDay `json:"traffic_30_days"`
	Traffic30In     int64              `json:"traffic_30_in"`
	Traffic30Out    int64              `json:"traffic_30_out"`
}

func (s *Server) mySummary(w http.ResponseWriter, r *http.Request, u store.User) {
	ds, err := s.store.ListDevices(r.Context(), &u.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	as, err := s.store.ListUserAllocations(r.Context(), u.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	days, err := s.store.UserTrafficDaily(r.Context(), u.ID, 30)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := summary{Computers: len(ds), Tunnels: len(as), Traffic30Days: days}
	for _, d := range ds {
		if d.Online {
			out.ComputersOnline++
		}
	}
	for _, a := range as {
		if s.cfg.Live != nil {
			if l, ok := s.cfg.Live.Live(a.DeviceID, a.Name); ok && l.Online {
				out.TunnelsLive++
			}
		}
	}
	for _, d := range days {
		out.Traffic30In += d.BytesIn
		out.Traffic30Out += d.BytesOut
	}
	if out.Traffic30Days == nil {
		out.Traffic30Days = []store.TrafficDay{}
	}
	writeJSON(w, http.StatusOK, out)
}
