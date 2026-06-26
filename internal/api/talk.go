package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/AkagiYui/kenko-nvr/internal/backchannel"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/onvif"
)

// handleTalk bridges browser microphone audio to a camera's RTSP/ONVIF back
// channel. The browser sends binary frames of 16-bit little-endian 8 kHz mono
// PCM over the WebSocket; the server converts them to G.711 and forwards them to
// the camera. Requires the operator role.
func (s *Server) handleTalk(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess == nil || roleRank(sess.Role) < roleRank(database.RoleOperator) {
		writeErr(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	id := chi.URLParam(r, "id")
	cam, err := s.db.Cameras.Get(id)
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}

	rtspURL, user, pass, err := s.cameraRTSPURL(cam)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sender, err := backchannel.New(rtspURL, user, pass, s.log)
	if err != nil {
		// Report the reason to the UI (e.g. camera has no back channel).
		_ = conn.WriteJSON(map[string]string{"error": err.Error()})
		return
	}
	defer sender.Close()
	_ = conn.WriteJSON(map[string]string{"status": "ready"})

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.BinaryMessage || len(data) == 0 {
			continue
		}
		if err := sender.WritePCM(data); err != nil {
			if s.log != nil {
				s.log.Debug("talk write failed", "camera", id, "err", err)
			}
			return
		}
	}
}

// cameraRTSPURL resolves the RTSP URL and media credentials used to reach a
// camera, resolving ONVIF sources to their stream URI.
func (s *Server) cameraRTSPURL(cam database.Camera) (rtspURL, username, password string, err error) {
	switch cam.SourceType {
	case database.SourceRTSP:
		return cam.URL, cam.Username, cam.Password, nil
	case database.SourceONVIF:
		dev, derr := onvif.Connect(cam.OnvifXAddr, cam.OnvifUsername, cam.OnvifPassword)
		if derr != nil {
			return "", "", "", fmt.Errorf("connecting to onvif device: %w", derr)
		}
		uri, uerr := dev.GetStreamURI(profileToken(dev, cam))
		if uerr != nil {
			return "", "", "", fmt.Errorf("resolving stream uri: %w", uerr)
		}
		return uri, cam.Username, cam.Password, nil
	default:
		return "", "", "", fmt.Errorf("two-way audio is only available for RTSP/ONVIF cameras")
	}
}
