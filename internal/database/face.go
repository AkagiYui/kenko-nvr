package database

// Face-recognition data model.
//
// A face is one detected face in one sampled video frame, carrying a 512-d
// L2-normalised ArcFace embedding (stored as a little-endian float32 BLOB). A
// person is a discovered identity; faces are grouped into a person via tracking
// + gallery matching (phase 2) and the grouping is correctable by hand
// (phase 3). A face_job is a unit of post-process work: analyse one recording.
//
// Timestamps are epoch-ms like everywhere else: Face.Timestamp is the absolute
// instant of the frame (recording start + in-file offset); OffsetMS is the
// frame's offset within its recording, used to seek playback.

// BBox is a face bounding box in source-frame pixels.
type BBox struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Person is a discovered identity that faces are grouped under.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Notes is free-form text the operator can attach.
	Notes string `json:"notes"`
	// CoverFaceID is the face whose thumbnail represents this person in the UI.
	CoverFaceID string `json:"coverFaceId"`
	// Named is true once an operator has given the person a name (vs an
	// auto-discovered, still-anonymous identity).
	Named bool `json:"named"`
	// FaceCount is a denormalised count of faces assigned to this person.
	FaceCount int     `json:"faceCount"`
	FirstSeen EpochMS `json:"firstSeen"`
	LastSeen  EpochMS `json:"lastSeen"`
	CreatedAt EpochMS `json:"createdAt"`
	UpdatedAt EpochMS `json:"updatedAt"`
}

// Face is one detected face in one sampled frame.
type Face struct {
	ID          string `json:"id"`
	RecordingID string `json:"recordingId"`
	CameraID    string `json:"cameraId"`
	// PersonID is the assigned identity, or "" while unassigned / under review.
	PersonID string `json:"personId"`
	// TrackID groups faces that the tracker linked as the same appearance within
	// one recording. "" until tracking (phase 2) runs.
	TrackID string `json:"trackId"`
	// Timestamp is the absolute instant of the frame; OffsetMS is its offset
	// within the recording (for seek-to-playback).
	Timestamp EpochMS `json:"ts"`
	OffsetMS  int64   `json:"offsetMs"`
	BBox      BBox    `json:"bbox"`
	DetScore  float64 `json:"detScore"`
	Quality   float64 `json:"quality"`
	// Embedding is the 512-d L2-normalised ArcFace vector. Biometric data: never
	// serialised to the API (json:"-"), stored as a float32 BLOB.
	Embedding []float32 `json:"-"`
	Dim       int       `json:"-"`
	Model     string    `json:"model"`
	// ThumbPath is the (optionally encrypted) cropped-face thumbnail, relative to
	// the faces dir. Set from phase 4. Not serialised; served via a dedicated
	// endpoint.
	ThumbPath string `json:"-"`
	// IsExemplar marks faces chosen as gallery exemplars for their person.
	IsExemplar bool `json:"isExemplar"`
	// Confirmed marks a face whose person assignment was set/locked by an
	// operator; clustering must not move it.
	Confirmed bool `json:"confirmed"`
	// Ignored marks a false positive / non-face / unwanted detection.
	Ignored   bool    `json:"ignored"`
	CreatedAt EpochMS `json:"createdAt"`
}

// LinkKind is the type of a clustering constraint.
type LinkKind string

const (
	// LinkMust forces two tracks into the same identity.
	LinkMust LinkKind = "must"
	// LinkCannot keeps two tracks in different identities.
	LinkCannot LinkKind = "cannot"
)

// PersonLink is an operator-supplied constraint between two tracks that
// re-clustering must honour.
type PersonLink struct {
	ID        string   `json:"id"`
	Kind      LinkKind `json:"kind"`
	ATrack    string   `json:"aTrack"`
	BTrack    string   `json:"bTrack"`
	CreatedAt EpochMS  `json:"createdAt"`
}

// FaceJobState is the lifecycle state of a face-analysis job.
type FaceJobState string

const (
	FaceJobPending FaceJobState = "pending"
	FaceJobRunning FaceJobState = "running"
	FaceJobDone    FaceJobState = "done"
	FaceJobFailed  FaceJobState = "failed"
	FaceJobSkipped FaceJobState = "skipped"
)

// FaceJob is a unit of post-process work: analyse one recording for faces.
type FaceJob struct {
	ID          string       `json:"id"`
	RecordingID string       `json:"recordingId"`
	State       FaceJobState `json:"state"`
	Attempts    int          `json:"attempts"`
	Error       string       `json:"error"`
	FaceCount   int          `json:"faceCount"`
	CreatedAt   EpochMS      `json:"createdAt"`
	UpdatedAt   EpochMS      `json:"updatedAt"`
}

// FaceConfig controls the face-recognition pipeline. Stored as JSON under the
// "face" settings key, edited at runtime in the web UI.
type FaceConfig struct {
	// Enabled turns the whole feature on: finalized recordings are enqueued for
	// analysis and the worker processes them.
	Enabled bool `json:"enabled"`
	// SidecarURL is the base URL of the inference sidecar (face-service).
	SidecarURL string `json:"sidecarURL"`
	// SampleFPS is how many frames per second to sample from a recording for
	// detection (faces don't need full frame rate).
	SampleFPS float64 `json:"sampleFps"`
	// MaxFramesPerJob caps frames analysed per recording (bounds CPU per job).
	MaxFramesPerJob int `json:"maxFramesPerJob"`
	// BatchSize is how many frames are sent to the sidecar per request.
	BatchSize int `json:"batchSize"`
	// AnalyzeWidth, when > 0, downscales sampled frames to this width before
	// sending (keeps aspect). 0 keeps the source resolution.
	AnalyzeWidth int `json:"analyzeWidth"`
	// MinFaceSize drops faces whose min(width,height) in pixels is below this.
	MinFaceSize int `json:"minFaceSize"`
	// DetThreshold drops detections with a lower detector score.
	DetThreshold float64 `json:"detThreshold"`
	// MinQuality drops faces below this computed quality (0 disables).
	MinQuality float64 `json:"minQuality"`
	// MotionGated, when true, restricts analysis to motion-event windows of a
	// recording when any exist (falling back to the whole file otherwise).
	MotionGated bool `json:"motionGated"`

	// --- gallery thresholds (used from phase 2) ---
	// MatchThreshold: cosine at/above which a track auto-joins an existing person.
	MatchThreshold float64 `json:"matchThreshold"`
	// ReviewThreshold: cosine below MatchThreshold but at/above this lands in the
	// human review queue instead of auto-creating a new identity.
	ReviewThreshold float64 `json:"reviewThreshold"`
}

// DefaultFaceConfig returns sane, disabled-by-default settings. Thresholds are
// starting points for buffalo_l (ArcFace w600k_r50) and should be calibrated on
// real cameras.
func DefaultFaceConfig() FaceConfig {
	return FaceConfig{
		Enabled:         false,
		SidecarURL:      "http://127.0.0.1:8077",
		SampleFPS:       2,
		MaxFramesPerJob: 1800,
		BatchSize:       16,
		AnalyzeWidth:    0,
		MinFaceSize:     50,
		DetThreshold:    0.5,
		MinQuality:      0,
		MotionGated:     true,
		MatchThreshold:  0.45,
		ReviewThreshold: 0.30,
	}
}
