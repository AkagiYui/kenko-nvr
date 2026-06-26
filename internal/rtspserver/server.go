// Package rtspserver re-publishes live cameras over RTSP so external clients
// (VLC, ffmpeg, other NVRs) can pull rtsp://host:8554/<cameraID>. It packetizes
// each camera's core.Stream into RTP on demand using gortsplib's server.
package rtspserver

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"log/slog"
	"strings"
	"sync"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// StreamProvider yields a camera's current live stream (nil if not live).
type StreamProvider interface {
	StreamFor(id string) *core.Stream
}

// Server is an on-demand RTSP publishing server.
type Server struct {
	Addr     string
	Provider StreamProvider
	Log      *slog.Logger

	srv  *gortsplib.Server
	mu   sync.Mutex
	pubs map[string]*publication // camera path -> publication
}

// Run starts the server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.pubs = make(map[string]*publication)
	s.srv = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: s.Addr,
	}
	if err := s.srv.Start(); err != nil {
		return err
	}
	if s.Log != nil {
		s.Log.Info("rtsp server listening", "addr", s.Addr)
	}
	go func() {
		<-ctx.Done()
		s.srv.Close()
	}()
	err := s.srv.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func cameraPath(p string) string { return strings.Trim(p, "/") }

// OnDescribe resolves the camera path to a (possibly newly created) publication.
func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	pub := s.ensurePub(cameraPath(ctx.Path))
	if pub == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, pub.stream, nil
}

// OnSetup returns the publication's stream for a reader session.
func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	pub := s.getPub(cameraPath(ctx.Path))
	if pub == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, pub.stream, nil
}

// OnPlay acknowledges playback.
func (s *Server) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *Server) getPub(path string) *publication {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubs[path]
}

// ensurePub returns the publication for a camera, creating and starting it if
// the camera is currently live.
func (s *Server) ensurePub(path string) *publication {
	s.mu.Lock()
	if p, ok := s.pubs[path]; ok {
		s.mu.Unlock()
		return p
	}
	s.mu.Unlock()

	src := s.Provider.StreamFor(path)
	if src == nil {
		return nil
	}
	pub, err := newPublication(s.srv, src)
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("rtsp publish setup failed", "camera", path, "err", err)
		}
		return nil
	}

	s.mu.Lock()
	// lost a race: another goroutine created it
	if existing, ok := s.pubs[path]; ok {
		s.mu.Unlock()
		pub.close()
		return existing
	}
	s.pubs[path] = pub
	s.mu.Unlock()

	pctx, cancel := context.WithCancel(context.Background())
	pub.cancel = cancel
	go func() {
		pub.run(pctx, src)
		s.mu.Lock()
		if s.pubs[path] == pub {
			delete(s.pubs, path)
		}
		s.mu.Unlock()
		pub.close()
	}()
	return pub
}

// rtpEncoder is the common interface of gortsplib's per-format RTP encoders.
type rtpEncoder interface {
	Encode(au [][]byte) ([]*rtp.Packet, error)
}

type encTrack struct {
	media   *description.Media
	enc     rtpEncoder
	trackID int
}

type publication struct {
	stream  *gortsplib.ServerStream
	byTrack map[int]*encTrack
	tsStart uint32
	cancel  context.CancelFunc
}

// newPublication builds a ServerStream (description + RTP encoders) from the
// source stream's tracks.
func newPublication(srv *gortsplib.Server, src *core.Stream) (*publication, error) {
	desc := &description.Session{}
	byTrack := make(map[int]*encTrack)

	for _, t := range src.Tracks() {
		var f format.Format
		var mediaType description.MediaType
		switch t.Codec {
		case core.CodecH264:
			f = &format.H264{PayloadTyp: 96, SPS: t.SPS, PPS: t.PPS, PacketizationMode: 1}
			mediaType = description.MediaTypeVideo
		case core.CodecH265:
			f = &format.H265{PayloadTyp: 96, VPS: t.VPS, SPS: t.SPS, PPS: t.PPS}
			mediaType = description.MediaTypeVideo
		case core.CodecAAC:
			if t.AudioConfig == nil {
				continue
			}
			f = &format.MPEG4Audio{
				PayloadTyp:       96,
				Config:           t.AudioConfig,
				SizeLength:       13,
				IndexLength:      3,
				IndexDeltaLength: 3,
			}
			mediaType = description.MediaTypeAudio
		default:
			continue
		}
		media := &description.Media{Type: mediaType, Formats: []format.Format{f}}
		desc.Medias = append(desc.Medias, media)

		enc, err := createEncoder(f)
		if err != nil {
			return nil, err
		}
		byTrack[t.ID] = &encTrack{media: media, enc: enc, trackID: t.ID}
	}

	stream := &gortsplib.ServerStream{Server: srv, Desc: desc}
	if err := stream.Initialize(); err != nil {
		return nil, err
	}
	return &publication{stream: stream, byTrack: byTrack, tsStart: randUint32()}, nil
}

func createEncoder(f format.Format) (rtpEncoder, error) {
	switch ff := f.(type) {
	case *format.H264:
		return ff.CreateEncoder()
	case *format.H265:
		return ff.CreateEncoder()
	case *format.MPEG4Audio:
		return ff.CreateEncoder()
	default:
		return nil, nil
	}
}

// run pumps the source stream into the ServerStream as RTP until ctx is
// cancelled or the source ends.
func (p *publication) run(ctx context.Context, src *core.Stream) {
	reader := src.AddReader(1024)
	defer reader.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-reader.Units():
			if !ok {
				return
			}
			et := p.byTrack[u.TrackID]
			if et == nil || et.enc == nil {
				continue
			}
			pkts, err := et.enc.Encode(u.AUs)
			if err != nil {
				continue
			}
			ts := p.tsStart + uint32(u.PTS)
			for _, pkt := range pkts {
				pkt.Timestamp = ts
				_ = p.stream.WritePacketRTP(et.media, pkt)
			}
		}
	}
}

func (p *publication) close() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.stream != nil {
		p.stream.Close()
	}
}

func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}
