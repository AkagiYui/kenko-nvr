package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/onvif"
)

func (s *Server) onvifDeviceFor(id string) (*onvif.Device, database.Camera, error) {
	cam, err := s.db.Cameras.Get(id)
	if err != nil {
		return nil, cam, err
	}
	dev, err := onvif.Connect(cam.OnvifXAddr, cam.OnvifUsername, cam.OnvifPassword)
	return dev, cam, err
}

// profileToken resolves the PTZ/media profile token to use, falling back to the
// camera's first profile when none is configured.
func profileToken(dev *onvif.Device, cam database.Camera) string {
	if cam.OnvifProfile != "" {
		return cam.OnvifProfile
	}
	if profiles, err := dev.GetProfiles(); err == nil && len(profiles) > 0 {
		return profiles[0].Token
	}
	return ""
}

func (s *Server) handlePTZ(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, err := s.db.Cameras.Get(id)
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	if !cam.OnvifEnabled || cam.OnvifXAddr == "" {
		writeErr(w, http.StatusBadRequest, "onvif control not enabled for this camera")
		return
	}

	var req struct {
		Action      string  `json:"action"` // move | stop | preset | absolute
		Pan         float64 `json:"pan"`
		Tilt        float64 `json:"tilt"`
		Zoom        float64 `json:"zoom"`
		PresetToken string  `json:"presetToken"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	dev, err := onvif.Connect(cam.OnvifXAddr, cam.OnvifUsername, cam.OnvifPassword)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	token := profileToken(dev, cam)

	switch req.Action {
	case "move":
		err = dev.ContinuousMove(token, req.Pan, req.Tilt, req.Zoom)
	case "stop":
		err = dev.Stop(token)
	case "absolute":
		err = dev.AbsoluteMove(token, req.Pan, req.Tilt, req.Zoom)
	case "preset":
		err = dev.GotoPreset(token, req.PresetToken)
	default:
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePTZPresets(w http.ResponseWriter, r *http.Request) {
	dev, cam, err := s.onvifDeviceFor(chi.URLParam(r, "id"))
	if err != nil {
		s.notFoundOr500(w, err)
		return
	}
	presets, err := dev.GetPresets(profileToken(dev, cam))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, presets)
}

func (s *Server) handleOnvifDiscover(w http.ResponseWriter, r *http.Request) {
	devices, err := onvif.Discover()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

// handleOnvifProbe connects to an ONVIF device and returns its profiles with
// resolved RTSP stream URIs, to help the user add a camera.
func (s *Server) handleOnvifProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		XAddr    string `json:"xaddr"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	dev, err := onvif.Connect(req.XAddr, req.Username, req.Password)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	profiles, err := dev.GetProfiles()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	type profileOut struct {
		Token     string `json:"token"`
		Name      string `json:"name"`
		StreamURI string `json:"streamUri"`
	}
	// Device info is best-effort: a camera may restrict GetDeviceInformation
	// while still serving profiles, which are the essential payload here.
	info, _ := dev.GetInfo()
	out := struct {
		Info     onvif.Info   `json:"info"`
		Profiles []profileOut `json:"profiles"`
	}{Info: info}

	for _, p := range profiles {
		uri, _ := dev.GetStreamURI(p.Token)
		out.Profiles = append(out.Profiles, profileOut{Token: p.Token, Name: p.Name, StreamURI: uri})
	}
	writeJSON(w, http.StatusOK, out)
}
