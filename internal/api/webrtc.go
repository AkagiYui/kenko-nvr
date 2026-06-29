package api

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/webrtc"
)

// handleWebRTC performs the WHEP-style SDP exchange for low-latency live view.
// The browser POSTs its SDP offer (request body); the server replies with the
// SDP answer and then streams H.264 video into the negotiated track.
func (s *Server) handleWebRTC(w http.ResponseWriter, r *http.Request) {
	webrtcCfg := s.mgr.SystemConfig().WebRTC
	if !webrtcCfg.Enabled {
		writeErr(w, http.StatusNotFound, "webrtc disabled")
		return
	}
	id := chi.URLParam(r, "id")

	offer, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || len(offer) == 0 {
		writeErr(w, http.StatusBadRequest, "missing SDP offer")
		return
	}

	stream, release, ok := s.mgr.LiveStreamFor(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "camera not live")
		return
	}

	answer, err := webrtc.Offer(stream, release, string(offer), webrtcCfg.STUNServers, s.log)
	if err != nil {
		release() // Offer only takes ownership of release on success
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/sdp")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, answer)
}
