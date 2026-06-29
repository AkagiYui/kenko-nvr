package core

import (
	"sync"
	"sync/atomic"
)

// Stream is a fan-out hub: a single source writes Units into it, and any number
// of Readers consume them. Writes never block on slow readers — a reader with a
// full buffer drops Units (and counts the drops) so a stalled recorder or HLS
// consumer can never wedge the ingest path.
type Stream struct {
	mu      sync.RWMutex
	tracks  []*Track
	readers map[*Reader]struct{}
	closed  bool

	// bytesIn accumulates the total media payload (sum of AU lengths) written to
	// this stream, used to compute the live ingest traffic rate.
	bytesIn atomic.Uint64
}

// NewStream creates a Stream carrying the given tracks.
func NewStream(tracks []*Track) *Stream {
	return &Stream{
		tracks:  tracks,
		readers: make(map[*Reader]struct{}),
	}
}

// Tracks returns the stream's tracks. The slice must not be mutated.
func (s *Stream) Tracks() []*Track {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tracks
}

// VideoTrack returns the first video track, or nil if there is none.
func (s *Stream) VideoTrack() *Track {
	for _, t := range s.Tracks() {
		if t.IsVideo() {
			return t
		}
	}
	return nil
}

// WriteUnit fans a Unit out to every reader. Called from the source goroutine.
func (s *Stream) WriteUnit(u *Unit) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	var n int
	for _, au := range u.AUs {
		n += len(au)
	}
	s.bytesIn.Add(uint64(n))
	for r := range s.readers {
		r.push(u)
	}
}

// BytesIn returns the cumulative media payload bytes written to this stream.
func (s *Stream) BytesIn() uint64 { return s.bytesIn.Load() }

// AddReader registers a new reader with the given buffer capacity.
func (s *Stream) AddReader(bufSize int) *Reader {
	if bufSize <= 0 {
		bufSize = 512
	}
	r := &Reader{
		stream: s,
		ch:     make(chan *Unit, bufSize),
	}
	s.mu.Lock()
	s.readers[r] = struct{}{}
	s.mu.Unlock()
	return r
}

// RemoveReader detaches a reader and closes its channel.
func (s *Stream) RemoveReader(r *Reader) {
	s.mu.Lock()
	if _, ok := s.readers[r]; ok {
		delete(s.readers, r)
		close(r.ch)
	}
	s.mu.Unlock()
}

// Close detaches all readers and marks the stream closed.
func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for r := range s.readers {
		delete(s.readers, r)
		close(r.ch)
	}
}

// Reader receives Units from a Stream.
type Reader struct {
	stream  *Stream
	ch      chan *Unit
	dropped atomic.Uint64
}

func (r *Reader) push(u *Unit) {
	select {
	case r.ch <- u:
	default:
		r.dropped.Add(1)
	}
}

// Units returns the channel of incoming units. It is closed when the reader is
// removed or the stream is closed.
func (r *Reader) Units() <-chan *Unit { return r.ch }

// Dropped returns the number of units dropped because the reader fell behind.
func (r *Reader) Dropped() uint64 { return r.dropped.Load() }

// Close removes the reader from its stream.
func (r *Reader) Close() {
	if r.stream != nil {
		r.stream.RemoveReader(r)
	}
}
