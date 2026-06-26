package gb28181

import "encoding/binary"

// MPEG-2 program-stream start codes (after the 00 00 01 prefix).
const (
	psPackStart    = 0xBA // pack_header
	psSystemHeader = 0xBB // system_header
	psMapStream    = 0xBC // program_stream_map (PSM)
	psPrivate1     = 0xBD // private_stream_1
	psPadding      = 0xBE // padding_stream
	psPrivate2     = 0xBF // private_stream_2
	psProgramEnd   = 0xB9 // MPEG_program_end_code
)

// PS stream_type values relevant to video codec detection.
const (
	streamTypeH264 = 0x1B
	streamTypeHEVC = 0x24
)

// psResult holds the elementary-stream payloads demuxed from one PS segment
// (typically one video picture's worth of data, reassembled from RTP).
type psResult struct {
	video []byte // concatenated video PES payloads (Annex-B elementary stream)
	audio []byte // concatenated audio PES payloads
	// videoCodec is "H264" or "H265" when a program_stream_map identified it,
	// otherwise "" (the caller then sniffs the NAL units).
	videoCodec string
	pts        int64
	hasPTS     bool
}

// demuxPS parses one MPEG-2 program-stream segment, returning the video and audio
// elementary-stream bytes it carries. It is tolerant of truncated/garbage input:
// on any malformed structure it resynchronises to the next start code.
func demuxPS(data []byte) psResult {
	var res psResult
	streamTypes := map[byte]byte{} // elementary_stream_id -> stream_type (from PSM)
	pos := 0
	n := len(data)

	for pos+4 <= n {
		// Find the next start-code prefix 00 00 01.
		if !(data[pos] == 0x00 && data[pos+1] == 0x00 && data[pos+2] == 0x01) {
			pos = nextStartCode(data, pos+1)
			if pos < 0 {
				break
			}
			continue
		}
		id := data[pos+3]
		switch {
		case id == psPackStart:
			// pack_header: 4-byte start + 10 fixed bytes; low 3 bits of the 14th
			// byte are the stuffing length.
			if pos+14 > n {
				return res
			}
			stuffing := int(data[pos+13] & 0x07)
			pos += 14 + stuffing

		case id == psSystemHeader || id == psMapStream || id == psPrivate2:
			if pos+6 > n {
				return res
			}
			length := int(binary.BigEndian.Uint16(data[pos+4 : pos+6]))
			body := pos + 6
			if body+length > n {
				length = n - body
			}
			if id == psMapStream {
				parsePSM(data[body:body+length], streamTypes)
			}
			pos = body + length

		case id == psPadding:
			if pos+6 > n {
				return res
			}
			length := int(binary.BigEndian.Uint16(data[pos+4 : pos+6]))
			pos += 6 + length

		case id == psProgramEnd:
			pos += 4

		case isPESVideo(id) || isPESAudio(id) || id == psPrivate1:
			payload, pts, hasPTS, next := parsePES(data, pos)
			if next < 0 {
				return res
			}
			if isPESVideo(id) {
				res.video = append(res.video, payload...)
				if hasPTS && !res.hasPTS {
					res.pts, res.hasPTS = pts, true
				}
				if st, ok := streamTypes[id]; ok {
					switch st {
					case streamTypeH264:
						res.videoCodec = "H264"
					case streamTypeHEVC:
						res.videoCodec = "H265"
					}
				}
			} else if isPESAudio(id) {
				res.audio = append(res.audio, payload...)
			}
			pos = next

		default:
			// Unknown start code: resync.
			pos = nextStartCode(data, pos+3)
			if pos < 0 {
				return res
			}
		}
	}
	return res
}

func isPESVideo(id byte) bool { return id >= 0xE0 && id <= 0xEF }
func isPESAudio(id byte) bool { return id >= 0xC0 && id <= 0xDF }

// parsePES parses a PES packet starting at pos (which points at the 00 00 01
// prefix), returning its payload, an optional PTS, and the offset just past it.
func parsePES(data []byte, pos int) (payload []byte, pts int64, hasPTS bool, next int) {
	n := len(data)
	if pos+6 > n {
		return nil, 0, false, -1
	}
	pktLen := int(binary.BigEndian.Uint16(data[pos+4 : pos+6]))
	hdr := pos + 6
	if hdr+3 > n {
		return nil, 0, false, -1
	}
	// Optional PES header: flags byte, PTS/DTS flags byte, header-data-length.
	ptsDtsFlags := (data[hdr+1] >> 6) & 0x03
	headerDataLen := int(data[hdr+2])
	payloadStart := hdr + 3 + headerDataLen
	if payloadStart > n {
		payloadStart = n
	}

	if ptsDtsFlags >= 0x02 && hdr+3+5 <= n {
		pts = readTimestamp(data[hdr+3 : hdr+3+5])
		hasPTS = true
	}

	// PES_packet_length covers everything after its own 2 bytes. A value of 0
	// (allowed for video) means "until the next start code".
	var payloadEnd int
	if pktLen == 0 {
		payloadEnd = nextStartCode(data, payloadStart)
		if payloadEnd < 0 {
			payloadEnd = n
		}
	} else {
		payloadEnd = hdr + pktLen
		if payloadEnd > n {
			payloadEnd = n
		}
	}
	if payloadEnd < payloadStart {
		payloadEnd = payloadStart
	}
	return data[payloadStart:payloadEnd], pts, hasPTS, payloadEnd
}

// readTimestamp decodes a 33-bit PTS/DTS field from its 5-byte representation.
func readTimestamp(b []byte) int64 {
	return (int64(b[0]&0x0E) << 29) |
		(int64(b[1]) << 22) |
		(int64(b[2]&0xFE) << 14) |
		(int64(b[3]) << 7) |
		(int64(b[4]) >> 1)
}

// parsePSM reads a program_stream_map body, filling streamTypes with the
// stream_type of each elementary_stream_id it declares.
func parsePSM(body []byte, streamTypes map[byte]byte) {
	if len(body) < 4 {
		return
	}
	// body: [version/flags 1][marker 1][program_stream_info_length 2]...
	infoLen := int(binary.BigEndian.Uint16(body[2:4]))
	p := 4 + infoLen
	if p+2 > len(body) {
		return
	}
	esMapLen := int(binary.BigEndian.Uint16(body[p : p+2]))
	p += 2
	end := p + esMapLen
	if end > len(body) {
		end = len(body)
	}
	for p+4 <= end {
		streamType := body[p]
		esID := body[p+1]
		esInfoLen := int(binary.BigEndian.Uint16(body[p+2 : p+4]))
		streamTypes[esID] = streamType
		p += 4 + esInfoLen
	}
}

// nextStartCode returns the index of the next 00 00 01 prefix at or after from,
// or -1 if none remains.
func nextStartCode(data []byte, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i+3 <= len(data); i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x01 {
			return i
		}
	}
	return -1
}
