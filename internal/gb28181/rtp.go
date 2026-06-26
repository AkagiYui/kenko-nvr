package gb28181

import "sort"

// rtpFrame is the reassembled payload of one media frame (all RTP packets that
// shared a timestamp), ready to be demuxed as a program-stream segment.
type rtpFrame struct {
	timestamp uint32
	data      []byte
}

type seqPayload struct {
	seq     uint16
	payload []byte
}

// maxPacketsPerFrame caps reassembly memory against a pathological sender.
const maxPacketsPerFrame = 8192

// frameAssembler groups incoming RTP packets into frames. A frame is flushed when
// the marker bit is seen or the RTP timestamp changes. Packets within a frame are
// reordered by sequence number (wrap-aware) before concatenation, tolerating mild
// network reordering.
type frameAssembler struct {
	have    bool
	ts      uint32
	pkts    []seqPayload
	onFrame func(rtpFrame)
}

// push adds one RTP packet to the assembler.
func (a *frameAssembler) push(seq uint16, ts uint32, marker bool, payload []byte) {
	if a.have && ts != a.ts {
		a.flush()
	}
	if !a.have {
		a.have = true
		a.ts = ts
	}
	if len(a.pkts) < maxPacketsPerFrame {
		// Copy: the caller reuses its receive buffer.
		cp := make([]byte, len(payload))
		copy(cp, payload)
		a.pkts = append(a.pkts, seqPayload{seq: seq, payload: cp})
	}
	if marker {
		a.flush()
	}
}

// flush emits the buffered packets as one frame.
func (a *frameAssembler) flush() {
	if !a.have || len(a.pkts) == 0 {
		a.have = false
		a.pkts = a.pkts[:0]
		return
	}
	pkts := a.pkts
	sort.SliceStable(pkts, func(i, j int) bool {
		return int16(pkts[i].seq-pkts[j].seq) < 0
	})
	var total int
	for _, p := range pkts {
		total += len(p.payload)
	}
	data := make([]byte, 0, total)
	for _, p := range pkts {
		data = append(data, p.payload...)
	}
	frame := rtpFrame{timestamp: a.ts, data: data}
	a.pkts = a.pkts[:0]
	a.have = false
	if a.onFrame != nil {
		a.onFrame(frame)
	}
}
