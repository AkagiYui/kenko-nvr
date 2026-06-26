package gb28181

import (
	"encoding/binary"
	"testing"

	"github.com/AkagiYui/kenko-nvr/internal/core"
)

func TestParseAndEncodeRegister(t *testing.T) {
	raw := "REGISTER sip:34020000002000000001@3402000000 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 192.168.1.10:5060;rport;branch=z9hG4bK1234\r\n" +
		"From: <sip:34020000001320000001@3402000000>;tag=abcd\r\n" +
		"To: <sip:34020000001320000001@3402000000>\r\n" +
		"Call-ID: 9999@192.168.1.10\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Contact: <sip:34020000001320000001@192.168.1.10:5060>\r\n" +
		"Expires: 3600\r\n" +
		"Content-Length: 0\r\n\r\n"

	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.IsResponse {
		t.Fatal("expected a request")
	}
	if m.Method != "REGISTER" {
		t.Fatalf("method = %q", m.Method)
	}
	if got := uriUser(m.Get("From")); got != "34020000001320000001" {
		t.Fatalf("from user = %q", got)
	}
	if got := headerParam(m.Get("From"), "tag"); got != "abcd" {
		t.Fatalf("from tag = %q", got)
	}
	if m.CSeqMethod() != "REGISTER" || m.CSeqNum() != 1 {
		t.Fatalf("cseq = %q %d", m.CSeqMethod(), m.CSeqNum())
	}
	if m.Get("Expires") != "3600" {
		t.Fatalf("expires = %q", m.Get("Expires"))
	}

	// Re-parse the encoded form to confirm a clean round trip.
	again, err := ParseMessage(m.Encode())
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if again.Method != "REGISTER" || again.CallID() != "9999@192.168.1.10" {
		t.Fatalf("round trip mismatch: %q %q", again.Method, again.CallID())
	}
}

func TestCompactHeaders(t *testing.T) {
	raw := "MESSAGE sip:x@y SIP/2.0\r\nv: SIP/2.0/UDP h;branch=z\r\nf: <sip:a@b>;tag=t\r\nt: <sip:c@d>\r\ni: call1\r\nl: 0\r\n\r\n"
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Get("Via") == "" || m.CallID() != "call1" {
		t.Fatalf("compact expansion failed: via=%q callid=%q", m.Get("Via"), m.CallID())
	}
	if uriUser(m.Get("From")) != "a" {
		t.Fatalf("from user = %q", uriUser(m.Get("From")))
	}
}

func TestVerifyDigest(t *testing.T) {
	const (
		user   = "34020000001320000001"
		realm  = "3402000000"
		nonce  = "0123456789abcdef"
		uri    = "sip:34020000002000000001@3402000000"
		pass   = "kenkopass"
		method = "REGISTER"
	)
	ha1 := md5hex(user + ":" + realm + ":" + pass)
	ha2 := md5hex(method + ":" + uri)
	resp := md5hex(ha1 + ":" + nonce + ":" + ha2)

	params := map[string]string{
		"username": user, "realm": realm, "nonce": nonce, "uri": uri, "response": resp,
	}
	if !verifyDigest(params, method, pass) {
		t.Fatal("valid digest rejected")
	}
	if verifyDigest(params, method, "wrongpass") {
		t.Fatal("wrong password accepted")
	}
}

func TestParseAuthParams(t *testing.T) {
	v := `Digest username="dev1", realm="r", nonce="n", uri="sip:x@y", response="abc", qop=auth, nc=00000001, cnonce="c"`
	p := parseAuthParams(v)
	if p["username"] != "dev1" || p["qop"] != "auth" || p["nc"] != "00000001" || p["cnonce"] != "c" {
		t.Fatalf("parsed = %+v", p)
	}
}

func TestParseCatalog(t *testing.T) {
	body := `<?xml version="1.0" encoding="GB2312"?>
<Response>
<CmdType>Catalog</CmdType>
<SN>1</SN>
<DeviceID>34020000001320000001</DeviceID>
<SumNum>2</SumNum>
<DeviceList Num="2">
<Item><DeviceID>34020000001310000001</DeviceID><Name>Door</Name><Status>ON</Status></Item>
<Item><DeviceID>34020000001310000002</DeviceID><Name>Yard</Name><Status>ON</Status></Item>
</DeviceList>
</Response>`
	m, err := parseGBMessage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if m.CmdType != "Catalog" || m.DeviceID != "34020000001320000001" {
		t.Fatalf("cmd=%q device=%q", m.CmdType, m.DeviceID)
	}
	if len(m.DeviceList.Items) != 2 {
		t.Fatalf("items = %d", len(m.DeviceList.Items))
	}
	if m.DeviceList.Items[0].DeviceID != "34020000001310000001" {
		t.Fatalf("item0 id = %q", m.DeviceList.Items[0].DeviceID)
	}
	if m.DeviceList.Items[1].Name != "Yard" {
		t.Fatalf("item1 name = %q", m.DeviceList.Items[1].Name)
	}
}

func TestParseKeepalive(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<Notify><CmdType>Keepalive</CmdType><SN>3</SN><DeviceID>34020000001320000001</DeviceID><Status>OK</Status></Notify>`
	m, err := parseGBMessage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if m.CmdType != "Keepalive" || m.Status != "OK" {
		t.Fatalf("cmd=%q status=%q", m.CmdType, m.Status)
	}
}

func TestMakeSSRCAndSDP(t *testing.T) {
	ssrc := makeSSRC("34020000001310000001", 7, false)
	if len(ssrc) != 10 || ssrc[0] != '0' {
		t.Fatalf("live ssrc = %q", ssrc)
	}
	if pb := makeSSRC("34020000001310000001", 7, true); pb[0] != '1' {
		t.Fatalf("playback ssrc lead = %q", pb)
	}
	sdp := string(buildInviteSDP("34020000001310000001", "192.168.1.5", 30002, ssrc))
	for _, want := range []string{"s=Play", "m=video 30002 RTP/AVP", "a=recvonly", "y=" + ssrc, "c=IN IP4 192.168.1.5"} {
		if !contains(sdp, want) {
			t.Fatalf("sdp missing %q in:\n%s", want, sdp)
		}
	}
}

func TestSplitAnnexB(t *testing.T) {
	// 3-byte and 4-byte start codes mixed.
	es := []byte{0, 0, 1, 0x67, 0xAA, 0, 0, 0, 1, 0x68, 0xBB, 0, 0, 1, 0x65, 0xCC, 0xDD}
	nals := splitAnnexB(es)
	if len(nals) != 3 {
		t.Fatalf("nals = %d (%v)", len(nals), nals)
	}
	if nals[0][0] != 0x67 || nals[1][0] != 0x68 || nals[2][0] != 0x65 {
		t.Fatalf("nal heads = %x %x %x", nals[0][0], nals[1][0], nals[2][0])
	}
	if len(nals[2]) != 3 {
		t.Fatalf("idr nal len = %d", len(nals[2]))
	}
}

func TestDemuxPSH264(t *testing.T) {
	annexb := []byte{
		0, 0, 0, 1, 0x67, 0x42, 0x00, 0x1f, // SPS
		0, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80, // PPS
		0, 0, 0, 1, 0x65, 0x11, 0x22, 0x33, // IDR slice
	}
	ps := buildTestPS(t, annexb, true)
	res := demuxPS(ps)
	if res.videoCodec != "H264" {
		t.Fatalf("codec = %q", res.videoCodec)
	}
	if !res.hasPTS {
		t.Fatal("expected a PTS")
	}
	nals := splitAnnexB(res.video)
	if len(nals) != 3 {
		t.Fatalf("video nals = %d", len(nals))
	}
	if sniffCodec(res, nals) != core.CodecH264 {
		t.Fatalf("sniff = %q", sniffCodec(res, nals))
	}
}

func TestFrameAssemblerReorder(t *testing.T) {
	var frames []rtpFrame
	a := &frameAssembler{onFrame: func(f rtpFrame) { frames = append(frames, f) }}
	// Same timestamp, out-of-order sequence, last has marker.
	a.push(2, 100, false, []byte{0xBB})
	a.push(1, 100, false, []byte{0xAA})
	a.push(3, 100, true, []byte{0xCC})
	if len(frames) != 1 {
		t.Fatalf("frames = %d", len(frames))
	}
	if got := frames[0].data; len(got) != 3 || got[0] != 0xAA || got[1] != 0xBB || got[2] != 0xCC {
		t.Fatalf("reassembled = %x", frames[0].data)
	}
}

func TestFrameAssemblerTimestampFlush(t *testing.T) {
	var frames []rtpFrame
	a := &frameAssembler{onFrame: func(f rtpFrame) { frames = append(frames, f) }}
	a.push(1, 100, false, []byte{0x01})
	a.push(2, 200, false, []byte{0x02}) // new timestamp flushes the first frame
	if len(frames) != 1 || frames[0].timestamp != 100 {
		t.Fatalf("expected one flushed frame ts=100, got %d frames", len(frames))
	}
}

// --- helpers -----------------------------------------------------------------

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// buildTestPS wraps an Annex-B elementary stream in a minimal MPEG-2 program
// stream: a pack header, an optional PSM declaring H.264, and one video PES with
// a PTS.
func buildTestPS(t *testing.T, annexb []byte, declareH264 bool) []byte {
	t.Helper()
	var ps []byte

	// pack_header: 4-byte start + 10 fixed bytes (last byte's low 3 bits = 0 stuffing).
	pack := []byte{0, 0, 1, psPackStart, 0x44, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	ps = append(ps, pack...)

	if declareH264 {
		psmBody := []byte{
			0x01, 0xFF, // version/marker
			0x00, 0x00, // program_stream_info_length = 0
			0x00, 0x04, // elementary_stream_map_length = 4
			streamTypeH264, 0xE0, 0x00, 0x00, // stream_type, es_id, es_info_len=0
			0x00, 0x00, 0x00, 0x00, // CRC32
		}
		psm := []byte{0, 0, 1, psMapStream}
		psm = appendLen(psm, len(psmBody))
		psm = append(psm, psmBody...)
		ps = append(ps, psm...)
	}

	// video PES with a PTS.
	ptsBytes := []byte{0x21, 0x00, 0x01, 0x00, 0x01} // PTS = 0
	pesHeader := []byte{0x80, 0x80, byte(len(ptsBytes))}
	pesPayload := append(append(append([]byte{}, pesHeader...), ptsBytes...), annexb...)
	pes := []byte{0, 0, 1, 0xE0}
	pes = appendLen(pes, len(pesPayload))
	pes = append(pes, pesPayload...)
	ps = append(ps, pes...)
	return ps
}

func appendLen(b []byte, n int) []byte {
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(n))
	return append(b, l[:]...)
}
