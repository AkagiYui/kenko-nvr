package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures the GB28181 SIP platform.
type Config struct {
	Enabled  bool
	SIPAddr  string // UDP listen address, e.g. ":5060"
	ServerID string // 20-digit platform/server ID (SIP server ID)
	Domain   string // SIP domain / realm, e.g. "3402000000"
	Password string // shared device registration password ("" disables auth)
	MediaIP  string // IP advertised to devices for media (auto-detected if empty)

	// MediaPortMin/Max bound the UDP ports used to receive RTP/PS media. Each
	// active live stream uses one port from this range.
	MediaPortMin int
	MediaPortMax int
}

func (c *Config) applyDefaults() {
	if c.SIPAddr == "" {
		c.SIPAddr = ":5060"
	}
	if c.ServerID == "" {
		c.ServerID = "34020000002000000001"
	}
	if c.Domain == "" {
		if len(c.ServerID) >= 10 {
			c.Domain = c.ServerID[:10]
		} else {
			c.Domain = "3402000000"
		}
	}
	if c.MediaPortMin == 0 {
		c.MediaPortMin = 30000
	}
	if c.MediaPortMax == 0 {
		c.MediaPortMax = 30500
	}
}

// Device is a registered GB28181 device (an IP camera or NVR) and its channels.
type Device struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Manufacturer string    `json:"manufacturer"`
	Model        string    `json:"model"`
	Online       bool      `json:"online"`
	LastSeen     time.Time `json:"lastSeen"`
	Channels     []Channel `json:"channels"`

	addr *net.UDPAddr
}

// Channel is one media channel exposed by a device.
type Channel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Status       string `json:"status"`
}

// Server is a GB28181 SIP platform that devices register to.
type Server struct {
	cfg Config
	log *slog.Logger

	conn *net.UDPConn

	mu       sync.Mutex
	devices  map[string]*Device
	pending  map[string]chan *Message // callID -> client-transaction inbox
	sessions map[string]chan struct{} // callID -> active media session cancel

	cseq      uint32
	sn        int32
	mediaPort int32
}

// New creates a GB28181 server.
func New(cfg Config, log *slog.Logger) *Server {
	cfg.applyDefaults()
	return &Server{
		cfg:       cfg,
		log:       log,
		devices:   make(map[string]*Device),
		pending:   make(map[string]chan *Message),
		sessions:  make(map[string]chan struct{}),
		mediaPort: int32(cfg.MediaPortMin),
	}
}

// Info returns the platform parameters a device must be configured with.
type Info struct {
	Enabled  bool   `json:"enabled"`
	ServerID string `json:"serverId"`
	Domain   string `json:"domain"`
	SIPAddr  string `json:"sipAddr"`
	MediaIP  string `json:"mediaIp"`
}

// Info reports the platform configuration for the UI.
func (s *Server) Info() Info {
	return Info{
		Enabled:  s.cfg.Enabled,
		ServerID: s.cfg.ServerID,
		Domain:   s.cfg.Domain,
		SIPAddr:  s.cfg.SIPAddr,
		MediaIP:  s.mediaIP(),
	}
}

// Devices returns a snapshot of registered devices and their channels.
func (s *Server) Devices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		cp := *d
		cp.Channels = append([]Channel(nil), d.Channels...)
		out = append(out, cp)
	}
	return out
}

// Run binds the SIP socket and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.SIPAddr)
	if err != nil {
		return fmt.Errorf("gb28181: resolve sip addr: %w", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("gb28181: listen sip: %w", err)
	}
	s.conn = conn
	if s.log != nil {
		s.log.Info("gb28181 sip server listening", "addr", s.cfg.SIPAddr, "serverID", s.cfg.ServerID)
	}

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go s.expireDevices(ctx)

	buf := make([]byte, maxUDPPayload)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("gb28181: sip read: %w", err)
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		go s.handleDatagram(data, src)
	}
}

func (s *Server) handleDatagram(data []byte, src *net.UDPAddr) {
	msg, err := ParseMessage(data)
	if err != nil {
		return
	}
	if msg.IsResponse {
		s.routeResponse(msg)
		return
	}
	switch msg.Method {
	case "REGISTER":
		s.handleRegister(msg, src)
	case "MESSAGE":
		s.handleMessage(msg, src)
	case "BYE":
		s.handleBye(msg, src)
	case "INVITE", "ACK", "INFO":
		s.respond(msg, src, 200, nil, nil)
	default:
		s.respond(msg, src, 200, nil, nil)
	}
}

func (s *Server) routeResponse(msg *Message) {
	callID := msg.CallID()
	s.mu.Lock()
	ch := s.pending[callID]
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default:
		}
	}
}

// --- REGISTER ----------------------------------------------------------------

func (s *Server) handleRegister(msg *Message, src *net.UDPAddr) {
	deviceID := uriUser(msg.Get("From"))
	if deviceID == "" {
		s.respond(msg, src, 400, nil, nil)
		return
	}

	if s.cfg.Password != "" {
		authz := msg.Get("Authorization")
		if authz == "" {
			extra := map[string]string{
				"WWW-Authenticate": wwwAuthenticate(s.cfg.Domain, genNonce()),
			}
			s.respond(msg, src, 401, extra, nil)
			return
		}
		if !verifyDigest(parseAuthParams(authz), "REGISTER", s.cfg.Password) {
			s.respond(msg, src, 403, nil, nil)
			return
		}
	}

	expired := msg.Get("Expires") == "0"
	if expired {
		s.mu.Lock()
		delete(s.devices, deviceID)
		s.mu.Unlock()
		s.respond(msg, src, 200, registerOKHeaders(), nil)
		if s.log != nil {
			s.log.Info("gb28181 device unregistered", "device", deviceID)
		}
		return
	}

	s.mu.Lock()
	d := s.devices[deviceID]
	if d == nil {
		d = &Device{ID: deviceID}
		s.devices[deviceID] = d
	}
	d.addr = src
	d.Online = true
	d.LastSeen = time.Now()
	s.mu.Unlock()

	s.respond(msg, src, 200, registerOKHeaders(), nil)
	if s.log != nil {
		s.log.Info("gb28181 device registered", "device", deviceID, "addr", src.String())
	}

	// Discover the device's channels and identity.
	go s.QueryCatalog(deviceID)
	go s.queryDeviceInfo(deviceID)
}

func registerOKHeaders() map[string]string {
	return map[string]string{
		"Date":    time.Now().Format("2006-01-02T15:04:05"),
		"Expires": "3600",
	}
}

// --- MESSAGE -----------------------------------------------------------------

func (s *Server) handleMessage(msg *Message, src *net.UDPAddr) {
	// Always acknowledge the MESSAGE transaction first.
	s.respond(msg, src, 200, nil, nil)

	body, err := parseGBMessage(msg.Body)
	if err != nil {
		return
	}
	deviceID := uriUser(msg.Get("From"))

	switch body.CmdType {
	case "Keepalive":
		s.touchDevice(deviceID, src)
	case "Catalog":
		s.applyCatalog(body)
	case "DeviceInfo":
		s.applyDeviceInfo(body)
	}
}

func (s *Server) touchDevice(deviceID string, src *net.UDPAddr) {
	if deviceID == "" {
		return
	}
	s.mu.Lock()
	d := s.devices[deviceID]
	if d == nil {
		d = &Device{ID: deviceID}
		s.devices[deviceID] = d
	}
	d.addr = src
	d.Online = true
	d.LastSeen = time.Now()
	s.mu.Unlock()
}

func (s *Server) applyCatalog(body *gbMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[body.DeviceID]
	if d == nil {
		return
	}
	for _, item := range body.DeviceList.Items {
		ch := Channel{
			ID:           item.DeviceID,
			Name:         item.Name,
			Manufacturer: item.Manufacturer,
			Model:        item.Model,
			Status:       item.Status,
		}
		replaced := false
		for i := range d.Channels {
			if d.Channels[i].ID == ch.ID {
				d.Channels[i] = ch
				replaced = true
				break
			}
		}
		if !replaced {
			d.Channels = append(d.Channels, ch)
		}
	}
}

func (s *Server) applyDeviceInfo(body *gbMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[body.DeviceID]
	if d == nil {
		return
	}
	if body.DeviceName != "" {
		d.Name = body.DeviceName
	}
	d.Manufacturer = body.Manufacturer
	d.Model = body.Model
}

func (s *Server) handleBye(msg *Message, src *net.UDPAddr) {
	s.respond(msg, src, 200, nil, nil)
	callID := msg.CallID()
	s.mu.Lock()
	if cancel := s.sessions[callID]; cancel != nil {
		close(cancel)
		delete(s.sessions, callID)
	}
	s.mu.Unlock()
}

// QueryCatalog asks a device to report its channel list.
func (s *Server) QueryCatalog(deviceID string) {
	d := s.device(deviceID)
	if d == nil || d.addr == nil {
		return
	}
	sn := int(atomic.AddInt32(&s.sn, 1))
	s.sendMessageRequest(d, buildCatalogQuery(deviceID, sn))
}

func (s *Server) queryDeviceInfo(deviceID string) {
	d := s.device(deviceID)
	if d == nil || d.addr == nil {
		return
	}
	sn := int(atomic.AddInt32(&s.sn, 1))
	s.sendMessageRequest(d, buildDeviceInfoQuery(deviceID, sn))
}

// --- helpers -----------------------------------------------------------------

func (s *Server) device(id string) *Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devices[id]
}

func (s *Server) expireDevices(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-2 * time.Minute)
			s.mu.Lock()
			for _, d := range s.devices {
				if d.LastSeen.Before(cutoff) {
					d.Online = false
				}
			}
			s.mu.Unlock()
		}
	}
}

// mediaIP returns the IP advertised to devices for media, auto-detecting the
// outbound interface address when not configured.
func (s *Server) mediaIP() string {
	if s.cfg.MediaIP != "" {
		return s.cfg.MediaIP
	}
	return outboundIP()
}

func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return "127.0.0.1"
}

func (s *Server) sipHost() string {
	host := s.mediaIP()
	_, port, err := net.SplitHostPort(s.cfg.SIPAddr)
	if err != nil || port == "" {
		port = "5060"
	}
	return net.JoinHostPort(host, port)
}

// respond sends a SIP response echoing the request's routing headers.
func (s *Server) respond(req *Message, src *net.UDPAddr, status int, extra map[string]string, body []byte) {
	resp := &Message{IsResponse: true, Status: status, Body: body}
	for _, v := range req.GetAll("Via") {
		resp.Add("Via", v)
	}
	resp.Set("From", req.Get("From"))
	to := req.Get("To")
	if headerParam(to, "tag") == "" {
		to += ";tag=" + genTag()
	}
	resp.Set("To", to)
	resp.Set("Call-ID", req.Get("Call-ID"))
	resp.Set("CSeq", req.Get("CSeq"))
	resp.Set("User-Agent", "kenko-nvr")
	for k, v := range extra {
		resp.Set(k, v)
	}
	if body != nil {
		resp.Set("Content-Type", "application/sdp")
	}
	if s.conn != nil {
		_, _ = s.conn.WriteToUDP(resp.Encode(), src)
	}
}

func genTag() string { return genNonce()[:8] }

func (s *Server) nextCSeq() uint32 { return atomic.AddUint32(&s.cseq, 1) + 1 }

// sendMessageRequest sends an out-of-dialog MESSAGE (a query) to a device.
func (s *Server) sendMessageRequest(d *Device, body []byte) {
	callID := genNonce()
	req := &Message{
		Method:     "MESSAGE",
		RequestURI: fmt.Sprintf("sip:%s@%s", d.ID, s.cfg.Domain),
		Body:       body,
	}
	req.Add("Via", fmt.Sprintf("SIP/2.0/UDP %s;rport;branch=z9hG4bK%s", s.sipHost(), genNonce()[:12]))
	req.Set("From", fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.ServerID, s.cfg.Domain, genTag()))
	req.Set("To", fmt.Sprintf("<sip:%s@%s>", d.ID, s.cfg.Domain))
	req.Set("Call-ID", callID)
	req.Set("CSeq", fmt.Sprintf("%d MESSAGE", s.nextCSeq()))
	req.Set("Max-Forwards", "70")
	req.Set("Content-Type", "application/MANSCDP+xml")
	req.Set("User-Agent", "kenko-nvr")
	if s.conn != nil {
		_, _ = s.conn.WriteToUDP(req.Encode(), d.addr)
	}
}
