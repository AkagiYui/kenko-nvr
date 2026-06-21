// Package recording writes live streams to fragmented-MP4 files and enforces
// retention (rolling deletion / storage thresholds).
package recording

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// RenderPath expands a path template into a relative recording path.
//
// Supported tokens:
//
//	{camera}     sanitized camera name
//	{camera_id}  camera ID
//	{year}       4-digit year       {month} 2-digit  {day} 2-digit
//	{hour}       2-digit (24h)      {minute} 2-digit {second} 2-digit
//	{unix}       unix seconds
//
// The result is always cleaned and forced to stay relative (no leading slash,
// no "..") so a malicious template can't escape the recordings root.
func RenderPath(template, cameraID, cameraName string, t time.Time) string {
	if template == "" {
		template = "{camera}/{year}-{month}-{day}/{camera}_{year}{month}{day}_{hour}{minute}{second}.mp4"
	}
	r := strings.NewReplacer(
		"{camera}", sanitize(cameraName),
		"{camera_id}", sanitize(cameraID),
		"{year}", fmt.Sprintf("%04d", t.Year()),
		"{month}", fmt.Sprintf("%02d", int(t.Month())),
		"{day}", fmt.Sprintf("%02d", t.Day()),
		"{hour}", fmt.Sprintf("%02d", t.Hour()),
		"{minute}", fmt.Sprintf("%02d", t.Minute()),
		"{second}", fmt.Sprintf("%02d", t.Second()),
		"{unix}", fmt.Sprintf("%d", t.Unix()),
	)
	out := r.Replace(template)

	// Normalise and confine to a relative path.
	out = filepath.ToSlash(filepath.Clean("/" + out))
	out = strings.TrimPrefix(out, "/")
	if !strings.HasSuffix(strings.ToLower(out), ".mp4") {
		out += ".mp4"
	}
	return out
}

// sanitize makes a string safe to use as a single path component.
func sanitize(s string) string {
	if s == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('_')
		default:
			// drop path separators and anything else unsafe
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "unnamed"
	}
	return out
}
