package api

import (
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/AkagiYui/kenko-nvr/internal/restream"
	"github.com/AkagiYui/kenko-nvr/internal/tsfeed"
)

// handleFLV serves an HTTP-FLV stream (consumable by flv.js, VLC, ffmpeg). It
// uses the browser-playable (H.264) live stream, transcoding H.265 on demand.
func (s *Server) handleFLV(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stream, release, ok := s.mgr.LiveStreamFor(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "camera not live")
		return
	}
	defer release()

	mux, err := restream.NewFlvMuxer(stream.Tracks())
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "video/x-flv")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(mux.Header()); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}

	reader := stream.AddReader(1024)
	defer reader.Close()
	for {
		select {
		case <-r.Context().Done():
			return
		case u, ok := <-reader.Units():
			if !ok {
				return
			}
			out, err := mux.Push(u)
			if err != nil {
				return
			}
			if out != nil {
				if _, err := w.Write(out); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}
}

// handleFLVWS serves the same FLV byte stream over a WebSocket, for flv.js
// configured with isLive + a ws:// URL.
func (s *Server) handleFLVWS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stream, release, ok := s.mgr.LiveStreamFor(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "camera not live")
		return
	}
	defer release()

	mux, err := restream.NewFlvMuxer(stream.Tracks())
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	write := func(data []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(websocket.BinaryMessage, data) == nil
	}
	if !write(mux.Header()) {
		return
	}

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
				return
			}
			out, err := mux.Push(u)
			if err != nil {
				return
			}
			if out != nil && !write(out) {
				return
			}
		}
	}
}

// handleTS serves a continuous MPEG-TS stream (VLC, ffmpeg). Unlike FLV it can
// carry the camera's original codec (including H.265) untouched.
func (s *Server) handleTS(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stream := s.mgr.StreamFor(id)
	if stream == nil {
		writeErr(w, http.StatusServiceUnavailable, "camera not live")
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	fw := &flushWriter{w: w}
	fw.f, _ = w.(http.Flusher)
	_ = tsfeed.Feed(r.Context(), stream, fw)
}

// flushWriter flushes the HTTP response after each write so the player receives
// media promptly instead of buffered.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}
