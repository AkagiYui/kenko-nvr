package gb28181

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

// mediaInactivityTimeout ends a session when no RTP arrives for this long, so the
// supervisor reconnects (re-INVITEs).
const mediaInactivityTimeout = 15 * time.Second

// Play invites a channel's live stream and publishes it into a core.Stream via
// onReady, blocking until ctx is cancelled, the device hangs up, or media stalls.
// It mirrors the contract of core.Source.Run.
func (s *Server) Play(ctx context.Context, deviceID, channelID string, onReady func(*core.Stream)) error {
	d := s.device(deviceID)
	if d == nil || d.addr == nil {
		return fmt.Errorf("gb28181: device %s not registered", deviceID)
	}
	if channelID == "" {
		channelID = deviceID
	}

	mediaConn, port, err := s.openMediaPort()
	if err != nil {
		return fmt.Errorf("gb28181: open media port: %w", err)
	}
	defer mediaConn.Close()

	callID := genNonce()
	fromTag := genTag()
	inviteCSeq := s.nextCSeq()
	ssrc := makeSSRC(channelID, int(inviteCSeq), false)
	sdp := buildInviteSDP(channelID, s.mediaIP(), port, ssrc)

	inbox := make(chan *Message, 8)
	s.mu.Lock()
	s.pending[callID] = inbox
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, callID)
		s.mu.Unlock()
	}()

	ok, err := s.sendInvite(ctx, d, channelID, callID, fromTag, inviteCSeq, ssrc, sdp, inbox)
	if err != nil {
		return err
	}
	toTag := headerParam(ok.Get("To"), "tag")
	s.sendACK(d, channelID, callID, fromTag, toTag, inviteCSeq)

	byeCh := make(chan struct{})
	s.mu.Lock()
	s.sessions[callID] = byeCh
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, callID)
		s.mu.Unlock()
		s.sendBYE(d, channelID, callID, fromTag, toTag)
	}()

	if s.log != nil {
		s.log.Info("gb28181 invite ok", "device", deviceID, "channel", channelID, "port", port)
	}

	return s.receiveMedia(ctx, mediaConn, byeCh, onReady)
}

// receiveMedia reads RTP, reassembles frames and feeds the media producer until
// the context ends, the device sends BYE, or media stalls.
func (s *Server) receiveMedia(ctx context.Context, conn *net.UDPConn, byeCh <-chan struct{}, onReady func(*core.Stream)) error {
	prod := &mediaProducer{log: s.log, onReady: onReady}
	asm := &frameAssembler{onFrame: prod.handleFrame}

	buf := make([]byte, 65535)
	lastRecv := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-byeCh:
			return fmt.Errorf("gb28181: device ended session")
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if time.Since(lastRecv) > mediaInactivityTimeout {
					return fmt.Errorf("gb28181: media inactivity timeout")
				}
				continue
			}
			return fmt.Errorf("gb28181: media read: %w", err)
		}
		lastRecv = time.Now()

		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		asm.push(pkt.SequenceNumber, pkt.Timestamp, pkt.Marker, pkt.Payload)
	}
}

// openMediaPort binds a UDP socket from the configured media port range.
func (s *Server) openMediaPort() (*net.UDPConn, int, error) {
	span := s.cfg.MediaPortMax - s.cfg.MediaPortMin + 1
	if span < 1 {
		span = 1
	}
	for i := 0; i < span; i++ {
		port := int(atomicNextPort(&s.mediaPort, s.cfg.MediaPortMin, s.cfg.MediaPortMax))
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err == nil {
			return conn, port, nil
		}
	}
	return nil, 0, fmt.Errorf("no free media port in %d-%d", s.cfg.MediaPortMin, s.cfg.MediaPortMax)
}

// sendInvite sends the INVITE and waits for a final response, retransmitting over
// UDP until a provisional/final response arrives or the attempt times out.
func (s *Server) sendInvite(ctx context.Context, d *Device, channelID, callID, fromTag string, cseq uint32, ssrc string, sdp []byte, inbox <-chan *Message) (*Message, error) {
	branch := "z9hG4bK" + genNonce()[:12]
	req := &Message{
		Method:     "INVITE",
		RequestURI: fmt.Sprintf("sip:%s@%s", channelID, s.cfg.Domain),
		Body:       sdp,
	}
	req.Add("Via", fmt.Sprintf("SIP/2.0/UDP %s;rport;branch=%s", s.sipHost(), branch))
	req.Set("From", fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.ServerID, s.cfg.Domain, fromTag))
	req.Set("To", fmt.Sprintf("<sip:%s@%s>", channelID, s.cfg.Domain))
	req.Set("Call-ID", callID)
	req.Set("CSeq", fmt.Sprintf("%d INVITE", cseq))
	req.Set("Contact", fmt.Sprintf("<sip:%s@%s>", s.cfg.ServerID, s.sipHost()))
	req.Set("Max-Forwards", "70")
	req.Set("Subject", fmt.Sprintf("%s:%s,%s:0", channelID, ssrc, s.cfg.ServerID))
	req.Set("Content-Type", "application/sdp")
	req.Set("User-Agent", "kenko-nvr")
	wire := req.Encode()

	deadline := time.NewTimer(16 * time.Second)
	defer deadline.Stop()
	retransmit := time.NewTimer(0)
	defer retransmit.Stop()
	interval := 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("gb28181: invite timed out")
		case <-retransmit.C:
			if s.conn != nil {
				_, _ = s.conn.WriteToUDP(wire, d.addr)
			}
			retransmit.Reset(interval)
			if interval < 4*time.Second {
				interval *= 2
			}
		case msg := <-inbox:
			if msg.Status < 200 {
				continue // 100 Trying / 180 Ringing
			}
			if msg.Status >= 300 {
				return nil, fmt.Errorf("gb28181: invite rejected: %d %s", msg.Status, msg.Reason)
			}
			return msg, nil
		}
	}
}

func (s *Server) sendACK(d *Device, channelID, callID, fromTag, toTag string, cseq uint32) {
	req := &Message{
		Method:     "ACK",
		RequestURI: fmt.Sprintf("sip:%s@%s", channelID, s.cfg.Domain),
	}
	req.Add("Via", fmt.Sprintf("SIP/2.0/UDP %s;rport;branch=z9hG4bK%s", s.sipHost(), genNonce()[:12]))
	req.Set("From", fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.ServerID, s.cfg.Domain, fromTag))
	req.Set("To", fmt.Sprintf("<sip:%s@%s>;tag=%s", channelID, s.cfg.Domain, toTag))
	req.Set("Call-ID", callID)
	req.Set("CSeq", fmt.Sprintf("%d ACK", cseq))
	req.Set("Max-Forwards", "70")
	req.Set("User-Agent", "kenko-nvr")
	if s.conn != nil {
		_, _ = s.conn.WriteToUDP(req.Encode(), d.addr)
	}
}

func (s *Server) sendBYE(d *Device, channelID, callID, fromTag, toTag string) {
	req := &Message{
		Method:     "BYE",
		RequestURI: fmt.Sprintf("sip:%s@%s", channelID, s.cfg.Domain),
	}
	req.Add("Via", fmt.Sprintf("SIP/2.0/UDP %s;rport;branch=z9hG4bK%s", s.sipHost(), genNonce()[:12]))
	req.Set("From", fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.ServerID, s.cfg.Domain, fromTag))
	req.Set("To", fmt.Sprintf("<sip:%s@%s>;tag=%s", channelID, s.cfg.Domain, toTag))
	req.Set("Call-ID", callID)
	req.Set("CSeq", fmt.Sprintf("%d BYE", s.nextCSeq()))
	req.Set("Max-Forwards", "70")
	req.Set("User-Agent", "kenko-nvr")
	if s.conn != nil {
		_, _ = s.conn.WriteToUDP(req.Encode(), d.addr)
	}
}

// atomicNextPort returns the next even port in [min,max], wrapping around.
func atomicNextPort(cursor *int32, min, max int) int32 {
	for {
		old := atomic.LoadInt32(cursor)
		next := old + 2
		if int(next) > max {
			next = int32(min)
		}
		if atomic.CompareAndSwapInt32(cursor, old, next) {
			return old
		}
	}
}
