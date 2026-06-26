// Package gb28181 implements a minimal GB/T 28181 (China national standard for
// video surveillance) SIP platform: IP cameras / NVRs register to it, and it can
// invite a channel's live stream (RTP/MPEG-PS over UDP), demux it into H.264 /
// H.265 access units and publish them into a core.Stream — the same media
// abstraction used by the RTSP and RTMP ingest paths.
//
// Everything here is pure Go (no CGO): SIP is a text protocol, the digest auth is
// crypto/md5, and the MPEG-PS demuxer is hand-written, so the CGO-free build is
// preserved.
//
// Scope: this implements the subset a recorder needs — device registration with
// digest auth, keepalive, catalog query, and INVITE/ACK/BYE for live preview over
// UDP. Historical playback, TCP media transport and cascading are intentionally
// out of scope for this first cut.
package gb28181

import (
	"fmt"
	"strconv"
	"strings"
)

// maxUDPPayload bounds a single SIP datagram we are willing to parse.
const maxUDPPayload = 65535

// Message is a parsed SIP message (request or response). Header names are stored
// canonicalised to their long form; the compact single-letter forms used by some
// devices are expanded on parse so lookups are uniform.
type Message struct {
	IsResponse bool

	// Request line (requests only).
	Method     string
	RequestURI string

	// Status line (responses only).
	Status int
	Reason string

	headers []header
	Body    []byte
}

type header struct {
	name  string
	value string
}

// compactHeaders maps the RFC 3261 single-letter compact header forms to their
// canonical long names.
var compactHeaders = map[string]string{
	"v": "Via",
	"f": "From",
	"t": "To",
	"i": "Call-ID",
	"m": "Contact",
	"l": "Content-Length",
	"c": "Content-Type",
	"s": "Subject",
	"k": "Supported",
	"o": "Allow-Events",
	"e": "Content-Encoding",
}

// canonHeaderNames maps a lowercased header name to its canonical spelling.
var canonHeaderNames = map[string]string{
	"via":              "Via",
	"from":             "From",
	"to":               "To",
	"call-id":          "Call-ID",
	"cseq":             "CSeq",
	"contact":          "Contact",
	"content-length":   "Content-Length",
	"content-type":     "Content-Type",
	"max-forwards":     "Max-Forwards",
	"expires":          "Expires",
	"user-agent":       "User-Agent",
	"www-authenticate": "WWW-Authenticate",
	"authorization":    "Authorization",
	"date":             "Date",
	"subject":          "Subject",
}

func canonName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if long, ok := compactHeaders[lower]; ok {
		return long
	}
	if c, ok := canonHeaderNames[lower]; ok {
		return c
	}
	// Title-case unknown headers for a tidy wire form.
	parts := strings.Split(lower, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

// ParseMessage parses a SIP message from a single datagram.
func ParseMessage(data []byte) (*Message, error) {
	if len(data) == 0 || len(data) > maxUDPPayload {
		return nil, fmt.Errorf("sip: bad message length %d", len(data))
	}
	// Split headers from body on the first blank line (CRLF CRLF, tolerating LF).
	text := string(data)
	idx := strings.Index(text, "\r\n\r\n")
	sep := 4
	if idx < 0 {
		idx = strings.Index(text, "\n\n")
		sep = 2
		if idx < 0 {
			idx = len(text)
			sep = 0
		}
	}
	head := text[:idx]
	var body []byte
	if sep > 0 && idx+sep <= len(text) {
		body = []byte(text[idx+sep:])
	}

	lines := splitLines(head)
	if len(lines) == 0 {
		return nil, fmt.Errorf("sip: empty message")
	}

	m := &Message{Body: body}
	if err := m.parseStartLine(lines[0]); err != nil {
		return nil, err
	}
	for _, ln := range lines[1:] {
		if ln == "" {
			continue
		}
		colon := strings.IndexByte(ln, ':')
		if colon < 0 {
			continue
		}
		name := canonName(ln[:colon])
		value := strings.TrimSpace(ln[colon+1:])
		m.headers = append(m.headers, header{name: name, value: value})
	}
	return m, nil
}

func (m *Message) parseStartLine(line string) error {
	if strings.HasPrefix(line, "SIP/2.0") {
		m.IsResponse = true
		rest := strings.TrimSpace(strings.TrimPrefix(line, "SIP/2.0"))
		code := rest
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			code = rest[:sp]
			m.Reason = strings.TrimSpace(rest[sp+1:])
		}
		n, err := strconv.Atoi(code)
		if err != nil {
			return fmt.Errorf("sip: bad status code %q", code)
		}
		m.Status = n
		return nil
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "SIP/2.0") {
		return fmt.Errorf("sip: bad request line %q", line)
	}
	m.Method = strings.ToUpper(parts[0])
	m.RequestURI = parts[1]
	return nil
}

// splitLines splits on CRLF or LF, unfolding header continuation lines (a line
// beginning with whitespace continues the previous one).
func splitLines(s string) []string {
	raw := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	for _, ln := range raw {
		if ln == "" {
			out = append(out, ln)
			continue
		}
		if (ln[0] == ' ' || ln[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += " " + strings.TrimSpace(ln)
			continue
		}
		out = append(out, ln)
	}
	return out
}

// Get returns the first value of a header (case-insensitive), or "".
func (m *Message) Get(name string) string {
	c := canonName(name)
	for _, h := range m.headers {
		if h.name == c {
			return h.value
		}
	}
	return ""
}

// GetAll returns every value of a header in order.
func (m *Message) GetAll(name string) []string {
	c := canonName(name)
	var out []string
	for _, h := range m.headers {
		if h.name == c {
			out = append(out, h.value)
		}
	}
	return out
}

// Set replaces all values of a header with a single value.
func (m *Message) Set(name, value string) {
	c := canonName(name)
	filtered := m.headers[:0:0]
	for _, h := range m.headers {
		if h.name != c {
			filtered = append(filtered, h)
		}
	}
	m.headers = append(filtered, header{name: c, value: value})
}

// Add appends a header value without removing existing ones (used for Via).
func (m *Message) Add(name, value string) {
	m.headers = append(m.headers, header{name: canonName(name), value: value})
}

// CSeqMethod returns the method named in the CSeq header (e.g. "INVITE").
func (m *Message) CSeqMethod() string {
	f := strings.Fields(m.Get("CSeq"))
	if len(f) >= 2 {
		return strings.ToUpper(f[1])
	}
	return ""
}

// CSeqNum returns the sequence number in the CSeq header.
func (m *Message) CSeqNum() int {
	f := strings.Fields(m.Get("CSeq"))
	if len(f) >= 1 {
		n, _ := strconv.Atoi(f[0])
		return n
	}
	return 0
}

// CallID returns the Call-ID header value.
func (m *Message) CallID() string { return m.Get("Call-ID") }

// Encode serialises the message to its wire form, fixing up Content-Length.
func (m *Message) Encode() []byte {
	var b strings.Builder
	if m.IsResponse {
		reason := m.Reason
		if reason == "" {
			reason = reasonText(m.Status)
		}
		fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", m.Status, reason)
	} else {
		fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", m.Method, m.RequestURI)
	}
	hasCL := false
	for _, h := range m.headers {
		if h.name == "Content-Length" {
			hasCL = true
			fmt.Fprintf(&b, "Content-Length: %d\r\n", len(m.Body))
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", h.name, h.value)
	}
	if !hasCL {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", len(m.Body))
	}
	b.WriteString("\r\n")
	out := []byte(b.String())
	return append(out, m.Body...)
}

func reasonText(code int) string {
	switch code {
	case 100:
		return "Trying"
	case 180:
		return "Ringing"
	case 200:
		return "OK"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 405:
		return "Method Not Allowed"
	case 408:
		return "Request Timeout"
	case 481:
		return "Call/Transaction Does Not Exist"
	case 500:
		return "Server Internal Error"
	default:
		return "OK"
	}
}

// --- header parameter helpers ------------------------------------------------

// headerParam extracts a ;name=value parameter from a header value (e.g. the tag
// from a From/To header, or the branch from a Via).
func headerParam(value, name string) string {
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if eq := strings.IndexByte(part, '='); eq >= 0 {
			if strings.EqualFold(strings.TrimSpace(part[:eq]), name) {
				return strings.Trim(strings.TrimSpace(part[eq+1:]), "\"")
			}
		}
	}
	return ""
}

// uriUser extracts the user part of the first SIP URI in a header value, i.e. the
// device or channel ID in "<sip:34020000001320000001@domain>".
func uriUser(value string) string {
	s := value
	if lt := strings.IndexByte(s, '<'); lt >= 0 {
		if gt := strings.IndexByte(s[lt:], '>'); gt >= 0 {
			s = s[lt+1 : lt+gt]
		}
	}
	s = strings.TrimPrefix(s, "sip:")
	s = strings.TrimPrefix(s, "sips:")
	if at := strings.IndexByte(s, '@'); at >= 0 {
		return s[:at]
	}
	if semi := strings.IndexByte(s, ';'); semi >= 0 {
		return s[:semi]
	}
	return s
}

// uriHostPort extracts host:port from the first SIP URI in a header value.
func uriHostPort(value string) string {
	s := value
	if lt := strings.IndexByte(s, '<'); lt >= 0 {
		if gt := strings.IndexByte(s[lt:], '>'); gt >= 0 {
			s = s[lt+1 : lt+gt]
		}
	}
	s = strings.TrimPrefix(s, "sip:")
	s = strings.TrimPrefix(s, "sips:")
	if at := strings.IndexByte(s, '@'); at >= 0 {
		s = s[at+1:]
	}
	if semi := strings.IndexByte(s, ';'); semi >= 0 {
		s = s[:semi]
	}
	return s
}
