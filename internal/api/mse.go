package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/AkagiYui/kenko-nvr/internal/mse"
)

// handleMSE streams a camera's live media to the browser as fragmented MP4 over
// a single WebSocket, for Media Source Extensions playback. One persistent
// connection replaces HLS's playlist polling and per-segment requests: the
// server pushes an init segment and then one fragment per GOP as it is produced.
func (s *Server) handleMSE(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// LiveStreamFor returns the source stream directly for H.264 cameras, or an
	// on-demand, viewer-shared H.264 transcode for non-H.264 cameras. release
	// detaches this viewer (and stops the shared transcode once nobody is left).
	stream, release, ok := s.mgr.LiveStreamFor(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "camera not live")
		return
	}
	defer release()
	frag := mse.NewFragmenter(stream.Tracks())
	initSeg, err := frag.InitSegment()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Detect client disconnect (and drain control frames).
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	write := func(mt int, data []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(mt, data) == nil
	}

	// Header (MIME codec, as text) then the binary init segment.
	hdr, _ := json.Marshal(map[string]string{"mimeCodec": frag.MimeCodec()})
	if !write(websocket.TextMessage, hdr) || !write(websocket.BinaryMessage, initSeg) {
		return
	}

	// A keyframe-delimited fragment can be large; a modest reader buffer absorbs
	// write jitter without dropping units (the stream drops, never blocks).
	reader := stream.AddReader(1024)
	defer reader.Close()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-closed:
			return
		case u, ok := <-reader.Units():
			if !ok {
				return // stream ended (camera reconnecting); client reconnects
			}
			out, err := frag.Push(u)
			if err != nil {
				if s.log != nil {
					s.log.Debug("mse fragment error", "camera", id, "err", err)
				}
				return
			}
			if out != nil && !write(websocket.BinaryMessage, out) {
				return
			}
		}
	}
}
