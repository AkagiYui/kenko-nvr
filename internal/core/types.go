// Package core defines the media abstractions shared by every subsystem:
// a Stream is a publish/subscribe hub that a Source (RTSP/RTMP) writes media
// Units into, and that consumers (the fMP4 recorder, the HLS muxer) read from.
//
// Keeping these types library-agnostic lets the RTSP path, the RTMP path, the
// recorder and the HLS muxer all interoperate through one small contract.
package core

import (
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
)

// MediaKind distinguishes video tracks from audio tracks.
type MediaKind int

const (
	// MediaVideo is a video track.
	MediaVideo MediaKind = iota
	// MediaAudio is an audio track.
	MediaAudio
)

// Codec identifies the compression format of a track.
type Codec string

const (
	// CodecH264 is H.264/AVC video.
	CodecH264 Codec = "H264"
	// CodecH265 is H.265/HEVC video.
	CodecH265 Codec = "H265"
	// CodecAAC is MPEG-4 AAC audio.
	CodecAAC Codec = "AAC"
)

// Track describes a single elementary stream within a Stream.
type Track struct {
	// ID is a stable 1-based identifier, also used as the MP4 track ID.
	ID    int
	Kind  MediaKind
	Codec Codec
	// ClockRate is the RTP/media timescale of PTS values for this track.
	ClockRate int

	// Video codec parameters (H264/H265). VPS is H265-only.
	SPS []byte
	PPS []byte
	VPS []byte

	// Audio codec parameters (AAC).
	AudioConfig *mpeg4audio.AudioSpecificConfig
}

// IsVideo reports whether the track carries video.
func (t *Track) IsVideo() bool { return t.Kind == MediaVideo }

// Unit is one timed chunk of media handed from a source to its readers.
//
// For video, AUs holds the NAL units of a single access unit (no Annex-B start
// codes, no length prefixes). For audio, AUs holds one or more raw frames.
type Unit struct {
	TrackID int
	// PTS is the presentation timestamp in the track's ClockRate units.
	PTS int64
	// NTP is the wall-clock time of the unit, used by HLS and recordings.
	NTP time.Time
	// RandomAccess marks an IDR/keyframe (video) usable as a segment boundary.
	RandomAccess bool
	// AUs are the access units / frames carried by this unit.
	AUs [][]byte
}
