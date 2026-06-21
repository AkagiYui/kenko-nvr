// Package rtmp implements an RTMP push-ingest server: encoders/cameras publish
// to rtmp://host:port/<app>/<streamKey>, and each published stream is demuxed
// from FLV into a core.Stream.
//
// Only the standard FLV codecs are supported on ingest: H.264 video and AAC
// audio. (Enhanced-RTMP H.265 is out of scope for now.)
package rtmp

import (
	"context"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/sirupsen/logrus"
	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

const videoClockRate = 90000

// PublishHandler is notified when streams start and stop publishing.
type PublishHandler interface {
	// OnPublishStart is called once a published stream's codecs are known.
	// Returning false rejects (closes) the publish.
	OnPublishStart(streamKey string, stream *core.Stream) bool
	// OnPublishStop is called when a previously started publish ends.
	OnPublishStop(streamKey string)
}

// Server is an RTMP ingest server.
type Server struct {
	Addr    string
	Log     *slog.Logger
	Handler PublishHandler
}

// Run listens and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}

	discard := logrus.New()
	discard.SetOutput(io.Discard)

	srv := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			return conn, &rtmp.ConnConfig{
				Handler:          &connHandler{server: s},
				Logger:           discard,
				ControlState:     rtmp.StreamControlStateConfig{DefaultBandwidthWindowSize: 6 * 1024 * 1024},
				ReaderBufferSize: 4 * 1024 * 1024,
				WriterBufferSize: 256 * 1024,
			}
		},
	})

	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = ln.Close()
	}()

	if s.Log != nil {
		s.Log.Info("rtmp server listening", "addr", s.Addr)
	}
	err = srv.Serve(ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// connHandler demuxes one publishing RTMP connection into a core.Stream.
type connHandler struct {
	rtmp.DefaultHandler
	server *Server

	streamKey string

	sps, pps    []byte
	audioConfig *mpeg4audio.AudioSpecificConfig

	stream       *core.Stream
	videoTrackID int
	audioTrackID int
	published    bool
}

func (h *connHandler) OnPublish(_ *rtmp.StreamContext, _ uint32, cmd *rtmpmsg.NetStreamPublish) error {
	h.streamKey = cmd.PublishingName
	if h.server.Log != nil {
		h.server.Log.Info("rtmp publish started", "key", h.streamKey)
	}
	return nil
}

func (h *connHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	data, err := io.ReadAll(payload)
	if err != nil || len(data) < 5 {
		return nil
	}
	codecID := data[0] & 0x0f
	if codecID != 7 { // 7 = AVC; H.265/enhanced-RTMP not supported
		return nil
	}
	frameType := data[0] >> 4 // 1 = keyframe
	avcPacketType := data[1]
	cts := int24(data[2:5])
	body := data[5:]

	switch avcPacketType {
	case 0: // sequence header (avcC)
		sps, pps, err := parseAVCDecoderConfig(body)
		if err != nil {
			if h.server.Log != nil {
				h.server.Log.Warn("bad avcC", "err", err)
			}
			return nil
		}
		h.sps, h.pps = sps, pps
	case 1: // NALU
		var avcc h264.AVCC
		if err := avcc.Unmarshal(body); err != nil {
			return nil
		}
		au := [][]byte(avcc)
		keyframe := frameType == 1

		if !h.published {
			if h.sps == nil {
				return nil // wait for the sequence header
			}
			h.publish()
		}
		if h.stream == nil {
			return nil
		}

		// Make keyframes self-contained by prepending parameter sets; this also
		// lets the recorder's DTS extractor parse the SPS in-band.
		if keyframe {
			au = append([][]byte{h.sps, h.pps}, au...)
		}
		pts := (int64(timestamp) + int64(cts)) * (videoClockRate / 1000)
		h.stream.WriteUnit(&core.Unit{
			TrackID:      h.videoTrackID,
			PTS:          pts,
			NTP:          time.Now(),
			RandomAccess: keyframe,
			AUs:          au,
		})
	}
	return nil
}

func (h *connHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	data, err := io.ReadAll(payload)
	if err != nil || len(data) < 2 {
		return nil
	}
	soundFormat := data[0] >> 4
	if soundFormat != 10 { // 10 = AAC
		return nil
	}
	aacPacketType := data[1]
	body := data[2:]

	switch aacPacketType {
	case 0: // AudioSpecificConfig
		var cfg mpeg4audio.AudioSpecificConfig
		if err := cfg.Unmarshal(body); err != nil {
			return nil
		}
		h.audioConfig = &cfg
	case 1: // raw AAC frame
		if !h.published || h.stream == nil || h.audioTrackID == 0 || h.audioConfig == nil {
			return nil
		}
		pts := int64(timestamp) * int64(h.audioConfig.SampleRate) / 1000
		h.stream.WriteUnit(&core.Unit{
			TrackID:      h.audioTrackID,
			PTS:          pts,
			NTP:          time.Now(),
			RandomAccess: true,
			AUs:          [][]byte{body},
		})
	}
	return nil
}

// publish builds the core.Stream from the captured codec parameters and offers
// it to the handler.
func (h *connHandler) publish() {
	var tracks []*core.Track
	id := 1

	h.videoTrackID = id
	tracks = append(tracks, &core.Track{
		ID:        id,
		Kind:      core.MediaVideo,
		Codec:     core.CodecH264,
		ClockRate: videoClockRate,
		SPS:       h.sps,
		PPS:       h.pps,
	})
	id++

	if h.audioConfig != nil {
		h.audioTrackID = id
		tracks = append(tracks, &core.Track{
			ID:          id,
			Kind:        core.MediaAudio,
			Codec:       core.CodecAAC,
			ClockRate:   h.audioConfig.SampleRate,
			AudioConfig: h.audioConfig,
		})
		id++
	}

	stream := core.NewStream(tracks)
	if h.server.Handler == nil || !h.server.Handler.OnPublishStart(h.streamKey, stream) {
		stream.Close()
		return
	}
	h.stream = stream
	h.published = true
}

func (h *connHandler) OnClose() {
	if h.published {
		if h.server.Handler != nil {
			h.server.Handler.OnPublishStop(h.streamKey)
		}
		if h.stream != nil {
			h.stream.Close()
		}
	}
	if h.server.Log != nil {
		h.server.Log.Info("rtmp publish ended", "key", h.streamKey)
	}
}
