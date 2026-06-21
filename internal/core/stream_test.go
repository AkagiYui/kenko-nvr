package core

import "testing"

func TestStreamFanOut(t *testing.T) {
	s := NewStream([]*Track{{ID: 1, Kind: MediaVideo, Codec: CodecH264}})
	r1 := s.AddReader(8)
	r2 := s.AddReader(8)

	s.WriteUnit(&Unit{TrackID: 1, PTS: 100})

	for i, r := range []*Reader{r1, r2} {
		select {
		case u := <-r.Units():
			if u.PTS != 100 {
				t.Errorf("reader %d got PTS %d", i, u.PTS)
			}
		default:
			t.Errorf("reader %d received nothing", i)
		}
	}
}

func TestStreamDropsWhenFull(t *testing.T) {
	s := NewStream(nil)
	r := s.AddReader(2)
	for i := 0; i < 10; i++ {
		s.WriteUnit(&Unit{PTS: int64(i)})
	}
	if r.Dropped() == 0 {
		t.Error("expected drops when reader buffer overflows")
	}
}

func TestStreamCloseClosesReaders(t *testing.T) {
	s := NewStream(nil)
	r := s.AddReader(4)
	s.Close()
	if _, ok := <-r.Units(); ok {
		t.Error("reader channel should be closed after stream Close")
	}
	// Writing after close must not panic.
	s.WriteUnit(&Unit{PTS: 1})
}

func TestRemoveReaderStopsDelivery(t *testing.T) {
	s := NewStream(nil)
	r := s.AddReader(4)
	r.Close()
	s.WriteUnit(&Unit{PTS: 1})
	if _, ok := <-r.Units(); ok {
		t.Error("removed reader channel should be closed")
	}
}

func TestVideoTrack(t *testing.T) {
	s := NewStream([]*Track{
		{ID: 1, Kind: MediaAudio, Codec: CodecAAC},
		{ID: 2, Kind: MediaVideo, Codec: CodecH265},
	})
	vt := s.VideoTrack()
	if vt == nil || vt.ID != 2 {
		t.Errorf("VideoTrack = %+v", vt)
	}
}
