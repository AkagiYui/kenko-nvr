package storage

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// encryptAll runs the full encrypt path and returns the object bytes
// (header + ciphertext) for a plaintext.
func encryptAll(t *testing.T, c *Cipher, plain []byte) []byte {
	t.Helper()
	r, err := c.EncryptReader(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("EncryptReader: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read encrypted: %v", err)
	}
	if len(out) != len(plain)+encHeaderLen {
		t.Fatalf("encrypted size = %d, want %d", len(out), len(plain)+encHeaderLen)
	}
	if string(out[:encMagicLen]) != encMagic {
		t.Fatalf("missing magic header")
	}
	return out
}

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := testCipher(t)
	for _, size := range []int{0, 1, 15, 16, 17, 4095, 4096, 4097, 10000} {
		plain := make([]byte, size)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		obj := encryptAll(t, c, plain)

		// Ciphertext must differ from plaintext (when non-empty).
		if size > 0 && bytes.Equal(obj[encHeaderLen:], plain) {
			t.Errorf("size %d: ciphertext equals plaintext", size)
		}

		dec, plainSize, err := c.DecryptingReadSeeker(bytes.NewReader(obj), int64(len(obj)))
		if err != nil {
			t.Fatalf("size %d: DecryptingReadSeeker: %v", size, err)
		}
		if plainSize != int64(size) {
			t.Errorf("size %d: plainSize = %d", size, plainSize)
		}
		got, err := io.ReadAll(dec)
		if err != nil {
			t.Fatalf("size %d: read: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("size %d: round trip mismatch", size)
		}
	}
}

// TestDecryptSeek is the core correctness test: decrypting an arbitrary byte
// range via Seek must equal the same slice of the plaintext. This exercises the
// CTR counter arithmetic and the intra-block keystream skip across block and
// 256-block (carry) boundaries.
func TestDecryptSeek(t *testing.T) {
	c := testCipher(t)
	plain := make([]byte, 9000)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	obj := encryptAll(t, c, plain)
	dec, _, err := c.DecryptingReadSeeker(bytes.NewReader(obj), int64(len(obj)))
	if err != nil {
		t.Fatal(err)
	}

	// Offsets chosen to hit block boundaries and the 256-block carry (byte 4096).
	offsets := []int64{0, 1, 15, 16, 17, 31, 32, 255, 256, 257, 4095, 4096, 4097, 8191, 8999, 9000}
	lengths := []int64{0, 1, 7, 16, 33, 100, 1000, 5000}
	for _, off := range offsets {
		for _, ln := range lengths {
			if _, err := dec.Seek(off, io.SeekStart); err != nil {
				t.Fatalf("seek %d: %v", off, err)
			}
			buf := make([]byte, ln)
			n, err := io.ReadFull(dec, buf)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				t.Fatalf("off=%d len=%d read: %v", off, ln, err)
			}
			want := plain[min64(off, 9000):min64(off+ln, 9000)]
			if !bytes.Equal(buf[:n], want) {
				t.Errorf("off=%d len=%d: got %d bytes, mismatch", off, ln, n)
			}
		}
	}
}

func TestDecryptSeekEndAndCurrent(t *testing.T) {
	c := testCipher(t)
	plain := []byte("0123456789abcdefghijABCDEFGHIJ")
	obj := encryptAll(t, c, plain)
	dec, size, err := c.DecryptingReadSeeker(bytes.NewReader(obj), int64(len(obj)))
	if err != nil {
		t.Fatal(err)
	}
	// SeekEnd is used by http.ServeContent to learn the size.
	if got, err := dec.Seek(0, io.SeekEnd); err != nil || got != size {
		t.Fatalf("SeekEnd = %d,%v want %d", got, err, size)
	}
	if _, err := dec.Seek(10, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	io.ReadFull(dec, buf)
	if string(buf) != "abcde" {
		t.Errorf("after SeekStart(10): %q", buf)
	}
	// SeekCurrent relative.
	if _, err := dec.Seek(5, io.SeekCurrent); err != nil { // now at 20
		t.Fatal(err)
	}
	io.ReadFull(dec, buf)
	if string(buf) != "ABCDE" {
		t.Errorf("after SeekCurrent: %q", buf)
	}
}

// TestServeContentDecrypted mirrors the playback path: http.ServeContent over
// the decrypting ReadSeeker must honour Range requests with correct plaintext.
func TestServeContentDecrypted(t *testing.T) {
	c := testCipher(t)
	plain := make([]byte, 5000)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	obj := encryptAll(t, c, plain)

	handler := func(w http.ResponseWriter, r *http.Request) {
		dec, _, err := c.DecryptingReadSeeker(bytes.NewReader(obj), int64(len(obj)))
		if err != nil {
			t.Fatal(err)
		}
		http.ServeContent(w, r, "clip.mp4", time.Unix(0, 0), dec)
	}

	// Full request.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler(rr, r)
	if rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), plain) {
		t.Fatalf("full: code=%d match=%v", rr.Code, bytes.Equal(rr.Body.Bytes(), plain))
	}

	// Range request (scrubbing).
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Range", "bytes=1000-1099")
	rr = httptest.NewRecorder()
	handler(rr, r)
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("range: code=%d", rr.Code)
	}
	if !bytes.Equal(rr.Body.Bytes(), plain[1000:1100]) {
		t.Errorf("range body mismatch")
	}
}

func TestDecryptWrongKeyAndBadMagic(t *testing.T) {
	c := testCipher(t)
	plain := []byte("sensitive recording bytes")
	obj := encryptAll(t, c, plain)

	// Wrong key decrypts to garbage (not the plaintext) but does not error
	// (CTR has no integrity check — documented tradeoff).
	other := testCipher(t)
	dec, _, err := other.DecryptingReadSeeker(bytes.NewReader(obj), int64(len(obj)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(dec)
	if bytes.Equal(got, plain) {
		t.Error("wrong key produced correct plaintext")
	}

	// Bad magic is rejected.
	bad := append([]byte("XXXX"), obj[encMagicLen:]...)
	if _, _, err := c.DecryptingReadSeeker(bytes.NewReader(bad), int64(len(bad))); err == nil {
		t.Error("expected error for bad magic")
	}

	// Truncated (smaller than header) is rejected.
	if _, _, err := c.DecryptingReadSeeker(bytes.NewReader([]byte("KNV")), 3); err == nil {
		t.Error("expected error for short object")
	}
}

func TestIVUniqueness(t *testing.T) {
	c := testCipher(t)
	plain := []byte("same plaintext")
	a := encryptAll(t, c, plain)
	b := encryptAll(t, c, plain)
	// Random IV per object -> different headers and different ciphertext.
	if bytes.Equal(a[encMagicLen:encHeaderLen], b[encMagicLen:encHeaderLen]) {
		t.Error("IV reused across objects")
	}
	if bytes.Equal(a[encHeaderLen:], b[encHeaderLen:]) {
		t.Error("ciphertext identical across objects (keystream reuse)")
	}
}

func TestDeriveKeyAndSalt(t *testing.T) {
	salt, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	salt2, _ := NewSalt()
	if salt == salt2 {
		t.Error("NewSalt not unique")
	}

	k1, err := DeriveKey("hunter2", salt)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("key len = %d", len(k1))
	}
	k2, _ := DeriveKey("hunter2", salt)
	if !bytes.Equal(k1, k2) {
		t.Error("DeriveKey not deterministic / cache broken")
	}
	k3, _ := DeriveKey("hunter2", salt2)
	if bytes.Equal(k1, k3) {
		t.Error("different salt produced same key")
	}
	k4, _ := DeriveKey("different", salt)
	if bytes.Equal(k1, k4) {
		t.Error("different passphrase produced same key")
	}
	if _, err := DeriveKey("", salt); err == nil {
		t.Error("expected error for empty passphrase")
	}
}

func TestAddBECarry(t *testing.T) {
	// Adding across a byte boundary.
	var iv [encIVLen]byte
	iv[15] = 0xff
	out := addBE(iv, 1)
	if out[15] != 0x00 || out[14] != 0x01 {
		t.Errorf("carry failed: %x", out)
	}
	// Full wraparound of the low 64 bits carries into the high half.
	for i := 8; i < 16; i++ {
		iv[i] = 0xff
	}
	out = addBE(iv, 1)
	if out[7] != 0x01 {
		t.Errorf("64-bit carry failed: %x", out)
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
