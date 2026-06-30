// Package face is the Go orchestration side of face recognition: it samples
// frames from recordings (via ffmpeg), sends them to the inference sidecar
// (face-service) for SCRFD detection + ArcFace embedding, and — from phase 2 —
// tracks, matches against the identity gallery and clusters. The sidecar does
// only stateless neural inference; all business logic and storage stay in Go,
// which keeps the main binary CGO-free.
package face

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Detection is one face the sidecar found in a frame. The embedding is the
// 512-d L2-normalised ArcFace vector (cosine similarity is a plain dot product).
type Detection struct {
	BBox      [4]float64   `json:"bbox"` // x1,y1,x2,y2 in source pixels
	DetScore  float64      `json:"det_score"`
	Kps       [][2]float64 `json:"kps"`
	Embedding []float32    `json:"embedding"`
	Sharpness float64      `json:"sharpness"`
	Pose      []float64    `json:"pose"` // yaw,pitch,roll (may be nil)
}

// Client talks to the face-service sidecar.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient builds a client for the sidecar base URL.
func NewClient(url string) *Client {
	return &Client{
		URL:  strings.TrimRight(url, "/"),
		HTTP: &http.Client{Timeout: 180 * time.Second},
	}
}

type frameReq struct {
	ID       int    `json:"id"`
	ImageB64 string `json:"image_b64"`
}

type analyzeReq struct {
	Frames  []frameReq `json:"frames"`
	MinFace int        `json:"min_face,omitempty"`
}

type frameResp struct {
	ID    int         `json:"id"`
	Faces []Detection `json:"faces"`
}

type analyzeResp struct {
	Frames []frameResp `json:"frames"`
	Model  string      `json:"model"`
	Dim    int         `json:"dim"`
}

// Analyze sends a batch of JPEG frames and returns, per input frame (aligned to
// the input slice order), the detected faces. minFace drops faces smaller than
// that many pixels at the sidecar (0 = no sidecar-side filter).
func (c *Client) Analyze(ctx context.Context, jpegs [][]byte, minFace int) (dets [][]Detection, model string, dim int, err error) {
	req := analyzeReq{MinFace: minFace, Frames: make([]frameReq, len(jpegs))}
	for i, j := range jpegs {
		req.Frames[i] = frameReq{ID: i, ImageB64: base64.StdEncoding.EncodeToString(j)}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, "", 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/analyze", bytes.NewReader(body))
	if err != nil {
		return nil, "", 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, "", 0, fmt.Errorf("sidecar /analyze: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var ar analyzeResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, "", 0, fmt.Errorf("decoding sidecar response: %w", err)
	}
	out := make([][]Detection, len(jpegs))
	for _, fr := range ar.Frames {
		if fr.ID >= 0 && fr.ID < len(out) {
			out[fr.ID] = fr.Faces
		}
	}
	return out, ar.Model, ar.Dim, nil
}

// Health is the sidecar's readiness/info report.
type Health struct {
	Status   string `json:"status"`
	Model    string `json:"model"`
	Dim      int    `json:"dim"`
	Provider string `json:"provider"`
}

// Health queries the sidecar's /healthz.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/healthz", nil)
	if err != nil {
		return h, err
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("sidecar /healthz: %s", resp.Status)
	}
	err = json.NewDecoder(resp.Body).Decode(&h)
	return h, err
}
