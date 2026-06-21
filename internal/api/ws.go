package api

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// The UI is served from the same origin; allow same-origin upgrades and,
	// for convenience on LAN setups, any origin (the API is token-protected).
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleWS streams periodic camera-status snapshots to the dashboard.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Drain client messages (and detect close) in the background.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Send an immediate snapshot, then on every tick.
	send := func() bool {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(s.mgr.AllStatus()) == nil
	}
	if !send() {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-closed:
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}
