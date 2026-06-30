package storage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Client-side encryption for recordings archived to S3.
//
// Recordings are encrypted with AES-256-CTR before upload and transparently
// decrypted on download/playback, so the object-storage provider only ever
// holds ciphertext. The threat model is provider-side confidentiality (a leaked
// bucket): CTR keeps the footage unreadable without the key.
//
// CTR is chosen over an AEAD whole-file mode because it is length-preserving and
// randomly seekable, so HTTP range requests (video scrubbing) fetch and decrypt
// only the requested span instead of re-downloading the whole object. The
// tradeoff is that CTR provides confidentiality but not integrity/authenticity;
// a malicious provider could tamper with bytes undetected. That is acceptable
// for the stated goal (prevent leakage) and keeps playback efficient.
//
// On-disk (in-bucket) object layout:
//
//	[ magic "KNV1" (4 bytes) ][ IV (16 bytes) ][ AES-256-CTR ciphertext ... ]
//
// The IV is random per object (never reused under one key). Plaintext length
// equals ciphertext length, so the plaintext size is objectSize - headerLen.

const (
	encMagic     = "KNV1"
	encMagicLen  = 4
	encIVLen     = aes.BlockSize // 16
	encHeaderLen = encMagicLen + encIVLen
)

// Cipher encrypts and decrypts recording bodies with a fixed AES-256 key.
type Cipher struct {
	block cipher.Block
}

// NewCipher builds a Cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("building aes cipher: %w", err)
	}
	return &Cipher{block: block}, nil
}

// EncryptReader wraps a plaintext reader, returning a reader that yields the
// encryption header (magic + random IV) followed by the AES-CTR ciphertext. The
// returned reader's total length is plaintextLen + encHeaderLen.
func (c *Cipher) EncryptReader(plaintext io.Reader) (io.Reader, error) {
	var iv [encIVLen]byte
	if _, err := rand.Read(iv[:]); err != nil {
		return nil, fmt.Errorf("generating iv: %w", err)
	}
	header := make([]byte, 0, encHeaderLen)
	header = append(header, encMagic...)
	header = append(header, iv[:]...)
	stream := cipher.NewCTR(c.block, iv[:])
	body := &cipher.StreamReader{S: stream, R: plaintext}
	return io.MultiReader(bytes.NewReader(header), body), nil
}

// EncryptedSize returns the object size for a plaintext of plainSize bytes.
func EncryptedSize(plainSize int64) int64 { return plainSize + encHeaderLen }

// DecryptingReadSeeker presents the decrypted plaintext of an encrypted object
// as a seekable stream suitable for http.ServeContent. src must be a seekable
// reader over the whole object (header + ciphertext); the returned reader maps
// plaintext offset p to object offset encHeaderLen+p and runs the CTR keystream
// from the correct position, so seeks (range requests) only touch the needed
// ciphertext span.
func (c *Cipher) DecryptingReadSeeker(src io.ReadSeeker, objectSize int64) (rs io.ReadSeeker, plainSize int64, err error) {
	if objectSize < encHeaderLen {
		return nil, 0, errors.New("encrypted object smaller than header")
	}
	header := make([]byte, encHeaderLen)
	if _, err := io.ReadFull(src, header); err != nil {
		return nil, 0, fmt.Errorf("reading encryption header: %w", err)
	}
	if string(header[:encMagicLen]) != encMagic {
		return nil, 0, errors.New("bad encryption header magic (object not encrypted with this scheme)")
	}
	var iv [encIVLen]byte
	copy(iv[:], header[encMagicLen:])
	d := &ctrDecryptor{
		src:     src,
		block:   c.block,
		iv:      iv,
		dataOff: encHeaderLen,
		size:    objectSize - encHeaderLen,
	}
	return d, d.size, nil
}

// ctrDecryptor is the seekable plaintext view over an encrypted object body.
type ctrDecryptor struct {
	src     io.ReadSeeker
	block   cipher.Block
	iv      [encIVLen]byte
	dataOff int64 // ciphertext start offset within the object
	size    int64 // plaintext size
	pos     int64 // current plaintext position
	stream  cipher.Stream
	aligned bool // whether stream + src are positioned at pos
}

func (d *ctrDecryptor) Read(p []byte) (int, error) {
	if d.pos >= d.size {
		return 0, io.EOF
	}
	if !d.aligned {
		if err := d.align(); err != nil {
			return 0, err
		}
	}
	if remaining := d.size - d.pos; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := d.src.Read(p)
	if n > 0 {
		d.stream.XORKeyStream(p[:n], p[:n])
		d.pos += int64(n)
	}
	if err == io.EOF && d.pos < d.size {
		// More plaintext expected: a short read from the source, keep going.
		err = nil
	}
	return n, err
}

// align positions the ciphertext source and the CTR keystream at d.pos.
func (d *ctrDecryptor) align() error {
	if _, err := d.src.Seek(d.dataOff+d.pos, io.SeekStart); err != nil {
		return err
	}
	blockIdx := uint64(d.pos) / aes.BlockSize
	skip := int(uint64(d.pos) % aes.BlockSize)
	counter := addBE(d.iv, blockIdx)
	d.stream = cipher.NewCTR(d.block, counter[:])
	if skip > 0 {
		// Advance the keystream to the exact byte within the block.
		discard := make([]byte, skip)
		d.stream.XORKeyStream(discard, discard)
	}
	d.aligned = true
	return nil
}

func (d *ctrDecryptor) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = d.pos + offset
	case io.SeekEnd:
		abs = d.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative seek position")
	}
	d.pos = abs
	d.aligned = false // rebuild keystream + source position on next Read
	return abs, nil
}

// addBE returns the big-endian 128-bit counter iv + add (with wraparound),
// matching how crypto/cipher's CTR increments the counter block.
func addBE(iv [encIVLen]byte, add uint64) [encIVLen]byte {
	out := iv
	for i := encIVLen - 1; i >= 0 && add > 0; i-- {
		sum := uint64(out[i]) + (add & 0xff)
		out[i] = byte(sum)
		add >>= 8
		add += sum >> 8 // carry out of this byte
	}
	return out
}

// --- key derivation ----------------------------------------------------------

// Argon2id parameters for deriving the AES key from the passphrase. Tuned for a
// long-running server: a single derivation per (passphrase, salt), cached.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
)

var (
	keyCacheMu sync.Mutex
	keyCache   = map[[32]byte][]byte{}
)

// DeriveKey derives a 32-byte AES key from a passphrase and base64 salt using
// Argon2id. Results are cached in memory so playback does not pay the KDF cost
// on every request.
func DeriveKey(passphrase, saltB64 string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("encryption passphrase is empty")
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return nil, fmt.Errorf("invalid encryption salt: %w", err)
	}
	if len(salt) < 16 {
		return nil, errors.New("encryption salt too short")
	}
	var ck [32]byte
	h := sha256.New()
	h.Write([]byte(passphrase))
	h.Write([]byte{0})
	h.Write(salt)
	copy(ck[:], h.Sum(nil))

	keyCacheMu.Lock()
	defer keyCacheMu.Unlock()
	if k, ok := keyCache[ck]; ok {
		return k, nil
	}
	k := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	keyCache[ck] = k
	return k, nil
}

// NewSalt returns a fresh random 16-byte salt, base64-encoded, for a new
// encryption configuration.
func NewSalt() (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(salt), nil
}
