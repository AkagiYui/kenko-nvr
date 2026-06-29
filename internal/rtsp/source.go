// Package rtsp implements an RTSP pull source: it connects to a camera's RTSP
// URL, decodes RTP into access units and publishes them into a core.Stream.
package rtsp

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/pion/rtp"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// Source is an RTSP pull source implementing core.Source.
type Source struct {
	URL         string
	Username    string
	Password    string
	Transport   string // "", "tcp", "udp"
	ReadTimeout time.Duration
	Log         *slog.Logger
}

// Run connects to the RTSP server and publishes media until ctx is cancelled
// or the connection fails.
func (s *Source) Run(ctx context.Context, onReady func(*core.Stream)) error {
	u, err := base.ParseURL(s.URL)
	if err != nil {
		return fmt.Errorf("invalid rtsp url: %w", err)
	}
	if s.Username != "" && u.User == nil {
		u.User = url.UserPassword(s.Username, s.Password)
	}

	readTimeout := s.ReadTimeout
	if readTimeout == 0 {
		readTimeout = 10 * time.Second
	}

	c := &gortsplib.Client{
		Scheme:      u.Scheme,
		Host:        u.Host,
		ReadTimeout: readTimeout,
	}
	switch s.Transport {
	case "tcp":
		p := gortsplib.ProtocolTCP
		c.Protocol = &p
	case "udp":
		p := gortsplib.ProtocolUDP
		c.Protocol = &p
	}

	if err := c.Start(); err != nil {
		return fmt.Errorf("starting client: %w", err)
	}
	defer c.Close()

	desc, _, err := c.Describe(u)
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}

	stream, binders, err := s.buildStream(desc)
	if err != nil {
		return err
	}
	if len(stream.Tracks()) == 0 {
		return fmt.Errorf("no supported media tracks found")
	}

	if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	for _, b := range binders {
		b(c, stream)
	}

	if _, err := c.Play(nil); err != nil {
		return fmt.Errorf("play: %w", err)
	}

	onReady(stream)
	if s.Log != nil {
		s.Log.Info("rtsp source playing", "url", redact(s.URL), "tracks", len(stream.Tracks()))
	}

	errCh := make(chan error, 1)
	go func() { errCh <- c.Wait() }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err == nil {
			err = fmt.Errorf("stream ended")
		}
		return err
	}
}

// binder wires an RTP callback for one media into the stream after setup.
type binder func(c *gortsplib.Client, stream *core.Stream)

// buildStream maps the RTSP description into core tracks and returns binders
// that register the per-format RTP decoders.
func (s *Source) buildStream(desc *description.Session) (*core.Stream, []binder, error) {
	var tracks []*core.Track
	var binders []binder
	nextID := 1

	for _, medi := range desc.Medias {
		for _, forma := range medi.Formats {
			switch f := forma.(type) {
			case *format.H264:
				track := &core.Track{
					ID:        nextID,
					Kind:      core.MediaVideo,
					Codec:     core.CodecH264,
					ClockRate: f.ClockRate(),
					SPS:       f.SPS,
					PPS:       f.PPS,
				}
				tracks = append(tracks, track)
				binders = append(binders, s.h264Binder(medi, f, track))
				nextID++

			case *format.H265:
				track := &core.Track{
					ID:        nextID,
					Kind:      core.MediaVideo,
					Codec:     core.CodecH265,
					ClockRate: f.ClockRate(),
					VPS:       f.VPS,
					SPS:       f.SPS,
					PPS:       f.PPS,
				}
				tracks = append(tracks, track)
				binders = append(binders, s.h265Binder(medi, f, track))
				nextID++

			case *format.MPEG4Audio:
				if f.Config == nil {
					continue
				}
				track := &core.Track{
					ID:          nextID,
					Kind:        core.MediaAudio,
					Codec:       core.CodecAAC,
					ClockRate:   f.ClockRate(),
					AudioConfig: f.Config,
				}
				tracks = append(tracks, track)
				binders = append(binders, s.aacBinder(medi, f, track))
				nextID++

			default:
				if s.Log != nil {
					s.Log.Debug("skipping unsupported rtsp format", "codec", forma.Codec())
				}
			}
		}
	}

	return core.NewStream(tracks), binders, nil
}

func (s *Source) h264Binder(medi *description.Media, f *format.H264, track *core.Track) binder {
	return func(c *gortsplib.Client, stream *core.Stream) {
		dec, err := f.CreateDecoder()
		if err != nil {
			s.logDecoderErr(err)
			return
		}
		c.OnPacketRTP(medi, f, func(pkt *rtp.Packet) {
			// Resolve the timestamp first, for every packet. gortsplib's RTP
			// time decoder only locks onto a track once it sees a packet whose
			// PTS==DTS (e.g. the start fragment of a keyframe, or a parameter
			// set). Decoding first and returning early on the incomplete
			// packets that make up a fragmented keyframe would starve it — it
			// would only ever see the AU-completing (end) fragment, never
			// resolve a PTS, and drop every video frame.
			pts, ok := c.PacketPTS(medi, pkt)
			if !ok {
				return
			}
			au, err := dec.Decode(pkt)
			if err != nil {
				return // fragmented packet not yet complete, or recoverable error
			}
			stream.WriteUnit(&core.Unit{
				TrackID:      track.ID,
				PTS:          pts,
				NTP:          time.Now(),
				RandomAccess: h264.IsRandomAccess(au),
				AUs:          au,
			})
		})
	}
}

func (s *Source) h265Binder(medi *description.Media, f *format.H265, track *core.Track) binder {
	return func(c *gortsplib.Client, stream *core.Stream) {
		dec, err := f.CreateDecoder()
		if err != nil {
			s.logDecoderErr(err)
			return
		}
		c.OnPacketRTP(medi, f, func(pkt *rtp.Packet) {
			// Resolve the timestamp before decoding — see h264Binder for why.
			pts, ok := c.PacketPTS(medi, pkt)
			if !ok {
				return
			}
			au, err := dec.Decode(pkt)
			if err != nil {
				return // fragmented packet not yet complete, or recoverable error
			}
			stream.WriteUnit(&core.Unit{
				TrackID:      track.ID,
				PTS:          pts,
				NTP:          time.Now(),
				RandomAccess: h265.IsRandomAccess(au),
				AUs:          au,
			})
		})
	}
}

func (s *Source) aacBinder(medi *description.Media, f *format.MPEG4Audio, track *core.Track) binder {
	return func(c *gortsplib.Client, stream *core.Stream) {
		dec, err := f.CreateDecoder()
		if err != nil {
			s.logDecoderErr(err)
			return
		}
		c.OnPacketRTP(medi, f, func(pkt *rtp.Packet) {
			pts, ok := c.PacketPTS(medi, pkt)
			if !ok {
				return
			}
			aus, err := dec.Decode(pkt)
			if err != nil {
				return
			}
			stream.WriteUnit(&core.Unit{
				TrackID:      track.ID,
				PTS:          pts,
				NTP:          time.Now(),
				RandomAccess: true,
				AUs:          aus,
			})
		})
	}
}

func (s *Source) logDecoderErr(err error) {
	if s.Log != nil {
		s.Log.Error("failed to create rtp decoder", "err", err)
	}
}

func redact(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.User != nil {
		u.User = url.UserPassword("***", "***")
	}
	return u.String()
}
