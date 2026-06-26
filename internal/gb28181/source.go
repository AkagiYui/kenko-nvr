package gb28181

import (
	"context"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// Source adapts a registered GB28181 channel to the core.Source interface, so a
// gb28181 camera is supervised by the manager exactly like an RTSP/RTMP one. Each
// Run invites the channel afresh, which means a device that re-registers (e.g.
// after a reboot) is picked up automatically on the next reconnect.
type Source struct {
	Server    *Server
	DeviceID  string
	ChannelID string
}

// Run invites the channel and publishes its media until ctx ends or the stream
// drops.
func (s *Source) Run(ctx context.Context, onReady func(*core.Stream)) error {
	return s.Server.Play(ctx, s.DeviceID, s.ChannelID, onReady)
}
