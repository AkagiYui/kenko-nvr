// Package webrtc serves a camera's live H.264 video over WebRTC for the lowest
// possible browser latency. It implements the WHEP-style exchange: the browser
// POSTs an SDP offer and receives an SDP answer; the server then pushes H.264
// access units into a local track.
//
// Audio is not sent: WebRTC requires Opus/G.711 and the core audio is AAC, so a
// v1 keeps WebRTC video-only (HLS/MSE remain available with audio).
package webrtc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

var annexBStartCode = []byte{0x00, 0x00, 0x00, 0x01}

// Offer negotiates a WebRTC session that streams the given live stream's H.264
// video. It returns the SDP answer. The session takes ownership of release and
// invokes it (plus closes the PeerConnection) when the connection ends.
//
// stream must carry an H.264 video track (use the manager's browser-playable
// LiveStreamFor, which transcodes H.265 on demand).
func Offer(stream *core.Stream, release func(), offerSDP string, stunServers []string, log *slog.Logger) (string, error) {
	video := stream.VideoTrack()
	if video == nil || video.Codec != core.CodecH264 {
		return "", fmt.Errorf("webrtc requires an H.264 video track")
	}

	cfg := webrtc.Configuration{}
	for _, s := range stunServers {
		cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{URLs: []string{s}})
	}

	pc, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		return "", err
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		"video", "kenko-nvr",
	)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	if _, err := pc.AddTrack(track); err != nil {
		_ = pc.Close()
		return "", err
	}

	// Clean up exactly once when the connection drops.
	cleanupCtx, cancel := context.WithCancel(context.Background())
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if log != nil {
			log.Debug("webrtc connection state", "state", state.String())
		}
		switch state {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed,
			webrtc.PeerConnectionStateDisconnected:
			cancel()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}); err != nil {
		_ = pc.Close()
		cancel()
		return "", err
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		cancel()
		return "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		cancel()
		return "", err
	}
	<-gatherComplete

	// Pump video until the connection drops.
	go func() {
		pumpVideo(cleanupCtx, stream, video, track, log)
		_ = pc.Close()
		release()
	}()

	return pc.LocalDescription().SDP, nil
}

// pumpVideo reads H.264 access units and writes them as WebRTC samples.
func pumpVideo(ctx context.Context, stream *core.Stream, video *core.Track, track *webrtc.TrackLocalStaticSample, log *slog.Logger) {
	reader := stream.AddReader(512)
	defer reader.Close()

	clock := int64(video.ClockRate)
	if clock == 0 {
		clock = 90000
	}
	var prevPTS int64
	havePrev := false

	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-reader.Units():
			if !ok {
				return
			}
			if u.TrackID != video.ID {
				continue
			}
			data := annexB(u, video)

			var dur time.Duration
			if havePrev {
				delta := u.PTS - prevPTS
				if delta > 0 {
					dur = time.Duration(delta) * time.Second / time.Duration(clock)
				}
			}
			if dur <= 0 {
				dur = time.Second / 30 // nominal first/garbled frame duration
			}
			prevPTS = u.PTS
			havePrev = true

			if err := track.WriteSample(media.Sample{Data: data, Duration: dur}); err != nil {
				if log != nil {
					log.Debug("webrtc write sample failed", "err", err)
				}
				return
			}
		}
	}
}

// annexB converts an access unit to Annex-B, prepending SPS/PPS on keyframes so
// a freshly joined decoder can start.
func annexB(u *core.Unit, video *core.Track) []byte {
	var nals [][]byte
	if u.RandomAccess {
		if len(video.SPS) > 0 {
			nals = append(nals, video.SPS)
		}
		if len(video.PPS) > 0 {
			nals = append(nals, video.PPS)
		}
	}
	nals = append(nals, u.AUs...)

	size := 0
	for _, n := range nals {
		size += len(annexBStartCode) + len(n)
	}
	out := make([]byte, 0, size)
	for _, n := range nals {
		out = append(out, annexBStartCode...)
		out = append(out, n...)
	}
	return out
}
