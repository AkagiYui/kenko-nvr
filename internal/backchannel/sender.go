// Package backchannel sends audio from the server to a camera over the ONVIF /
// RTSP back channel, enabling browser-to-camera two-way talk. The browser
// captures the microphone, downsamples to 8 kHz mono PCM, and streams it here;
// this package converts it to G.711 (the codec cameras expose on their back
// channel) and writes RTP to the camera.
package backchannel

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/url"
	"sync"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/g711"
	"github.com/pion/rtp"
)

// g711Encoder is the subset of gortsplib's G.711 RTP encoder we use.
type g711Encoder interface {
	Encode(samples []byte) ([]*rtp.Packet, error)
}

// Sender holds an open RTSP back-channel session to one camera.
type Sender struct {
	client *gortsplib.Client
	media  *description.Media
	enc    g711Encoder
	mulaw  bool

	mu          sync.Mutex
	randomStart uint32
	pts         int64
	closed      bool
}

// New opens a back-channel session to the camera at rtspURL. It returns an error
// (and the talk feature is unavailable) if the camera exposes no G.711 back
// channel, which many cameras do not.
func New(rtspURL, username, password string, log *slog.Logger) (*Sender, error) {
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return nil, fmt.Errorf("invalid rtsp url: %w", err)
	}
	if username != "" && u.User == nil {
		u.User = url.UserPassword(username, password)
	}

	c := &gortsplib.Client{
		Scheme:              u.Scheme,
		Host:                u.Host,
		RequestBackChannels: true,
	}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("starting rtsp client: %w", err)
	}

	desc, _, err := c.Describe(u)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("describe: %w", err)
	}

	medi, forma := findG711BackChannel(desc)
	if medi == nil {
		c.Close()
		return nil, fmt.Errorf("camera exposes no G.711 back channel (two-way audio unsupported)")
	}

	if _, err := c.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		c.Close()
		return nil, fmt.Errorf("setup back channel: %w", err)
	}
	if _, err := c.Play(nil); err != nil {
		c.Close()
		return nil, fmt.Errorf("play: %w", err)
	}

	enc, err := forma.CreateEncoder()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("create g711 encoder: %w", err)
	}

	if log != nil {
		log.Info("backchannel opened", "mulaw", forma.MULaw)
	}
	return &Sender{
		client:      c,
		media:       medi,
		enc:         enc,
		mulaw:       forma.MULaw,
		randomStart: randUint32(),
	}, nil
}

// WritePCM converts 16-bit little-endian 8 kHz mono LPCM to the camera's G.711
// flavour and sends it as RTP.
func (s *Sender) WritePCM(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("sender closed")
	}

	var samples []byte
	var err error
	if s.mulaw {
		samples, err = g711.Mulaw(pcm).Marshal()
	} else {
		samples, err = g711.Alaw(pcm).Marshal()
	}
	if err != nil {
		return err
	}

	pkts, err := s.enc.Encode(samples)
	if err != nil {
		return err
	}
	for _, pkt := range pkts {
		pkt.Timestamp += s.randomStart + uint32(s.pts)
		if err := s.client.WritePacketRTP(s.media, pkt); err != nil {
			return err
		}
	}
	s.pts += int64(len(samples)) // G.711 = 1 byte per 8 kHz sample
	return nil
}

// Close ends the session.
func (s *Sender) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.client.Close()
}

func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

func findG711BackChannel(desc *description.Session) (*description.Media, *format.G711) {
	for _, media := range desc.Medias {
		if !media.IsBackChannel {
			continue
		}
		for _, forma := range media.Formats {
			if g, ok := forma.(*format.G711); ok {
				return media, g
			}
		}
	}
	return nil, nil
}
