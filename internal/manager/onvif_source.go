package manager

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/AkagiYui/kenko-nvr/internal/core"
	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/onvif"
	"github.com/AkagiYui/kenko-nvr/internal/rtsp"
)

// onvifSource is a core.Source backed by ONVIF: on each connection attempt it
// queries the device for the RTSP stream URI of the selected (or first) media
// profile, then pulls the stream over RTSP. Resolving at connect time means a
// camera whose RTSP URL changes is picked up automatically on reconnect.
type onvifSource struct {
	cam       database.Camera
	transport string
	log       *slog.Logger
}

func (s *onvifSource) Run(ctx context.Context, onReady func(*core.Stream)) error {
	dev, err := onvif.Connect(s.cam.OnvifXAddr, s.cam.OnvifUsername, s.cam.OnvifPassword)
	if err != nil {
		return fmt.Errorf("onvif connect: %w", err)
	}

	token := s.cam.OnvifProfile
	if token == "" {
		profiles, err := dev.GetProfiles()
		if err != nil {
			return fmt.Errorf("onvif get profiles: %w", err)
		}
		if len(profiles) == 0 {
			return fmt.Errorf("onvif device exposes no media profiles")
		}
		token = profiles[0].Token
	}

	uri, err := dev.GetStreamURI(token)
	if err != nil {
		return fmt.Errorf("onvif get stream uri: %w", err)
	}

	// RTSP stream authentication: prefer explicit media credentials, otherwise
	// reuse the ONVIF credentials (cameras typically share one account).
	user, pass := s.cam.Username, s.cam.Password
	if user == "" {
		user, pass = s.cam.OnvifUsername, s.cam.OnvifPassword
	}

	if s.log != nil {
		s.log.Info("onvif resolved stream", "camera", s.cam.ID, "profile", token)
	}

	rtspSource := &rtsp.Source{
		URL:       uri,
		Username:  user,
		Password:  pass,
		Transport: s.transport,
		Log:       s.log,
	}
	return rtspSource.Run(ctx, onReady)
}
