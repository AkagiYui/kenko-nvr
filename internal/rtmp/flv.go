package rtmp

import "fmt"

// parseAVCDecoderConfig extracts the first SPS and PPS from an
// AVCDecoderConfigurationRecord (the avcC box carried in an FLV AVC sequence
// header).
func parseAVCDecoderConfig(b []byte) (sps, pps []byte, err error) {
	if len(b) < 7 {
		return nil, nil, fmt.Errorf("avcC too short")
	}
	if b[0] != 1 {
		return nil, nil, fmt.Errorf("unsupported avcC version %d", b[0])
	}
	pos := 5

	numSPS := int(b[pos] & 0x1f)
	pos++
	for i := 0; i < numSPS; i++ {
		if pos+2 > len(b) {
			return nil, nil, fmt.Errorf("avcC truncated SPS length")
		}
		l := int(b[pos])<<8 | int(b[pos+1])
		pos += 2
		if pos+l > len(b) {
			return nil, nil, fmt.Errorf("avcC truncated SPS")
		}
		if i == 0 {
			sps = append([]byte(nil), b[pos:pos+l]...)
		}
		pos += l
	}

	if pos >= len(b) {
		return sps, nil, fmt.Errorf("avcC missing PPS")
	}
	numPPS := int(b[pos])
	pos++
	for i := 0; i < numPPS; i++ {
		if pos+2 > len(b) {
			return sps, nil, fmt.Errorf("avcC truncated PPS length")
		}
		l := int(b[pos])<<8 | int(b[pos+1])
		pos += 2
		if pos+l > len(b) {
			return sps, nil, fmt.Errorf("avcC truncated PPS")
		}
		if i == 0 {
			pps = append([]byte(nil), b[pos:pos+l]...)
		}
		pos += l
	}

	if sps == nil || pps == nil {
		return sps, pps, fmt.Errorf("avcC missing SPS or PPS")
	}
	return sps, pps, nil
}

// int24 decodes a signed big-endian 24-bit integer (FLV composition time).
func int24(b []byte) int32 {
	v := int32(b[0])<<16 | int32(b[1])<<8 | int32(b[2])
	if v&0x800000 != 0 {
		v |= ^0xffffff // sign-extend
	}
	return v
}
