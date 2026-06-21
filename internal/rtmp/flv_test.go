package rtmp

import (
	"bytes"
	"testing"
)

func TestParseAVCDecoderConfig(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1e}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}

	var b bytes.Buffer
	b.WriteByte(1)    // version
	b.WriteByte(0x42) // profile
	b.WriteByte(0x00) // compat
	b.WriteByte(0x1e) // level
	b.WriteByte(0xff) // lengthSizeMinusOne (6 bits set | 3)
	b.WriteByte(0xe1) // numSPS = 1 (3 bits reserved | 1)
	b.WriteByte(byte(len(sps) >> 8))
	b.WriteByte(byte(len(sps)))
	b.Write(sps)
	b.WriteByte(1) // numPPS
	b.WriteByte(byte(len(pps) >> 8))
	b.WriteByte(byte(len(pps)))
	b.Write(pps)

	gotSPS, gotPPS, err := parseAVCDecoderConfig(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSPS, sps) {
		t.Errorf("SPS = %x, want %x", gotSPS, sps)
	}
	if !bytes.Equal(gotPPS, pps) {
		t.Errorf("PPS = %x, want %x", gotPPS, pps)
	}
}

func TestParseAVCDecoderConfigErrors(t *testing.T) {
	if _, _, err := parseAVCDecoderConfig([]byte{0x00, 0x01}); err == nil {
		t.Error("expected error for too-short record")
	}
	if _, _, err := parseAVCDecoderConfig([]byte{2, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("expected error for unsupported version")
	}
}

func TestInt24(t *testing.T) {
	cases := map[int32][]byte{
		0:    {0x00, 0x00, 0x00},
		1:    {0x00, 0x00, 0x01},
		1000: {0x00, 0x03, 0xe8},
		-1:   {0xff, 0xff, 0xff},
		-256: {0xff, 0xff, 0x00},
	}
	for want, b := range cases {
		if got := int24(b); got != want {
			t.Errorf("int24(%x) = %d, want %d", b, got, want)
		}
	}
}
