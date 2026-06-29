package database

import (
	"encoding/json"
	"testing"
	"time"
)

// TestEpochMSWireFormat pins the API contract: time fields cross the wire as
// absolute Unix-millisecond numbers (or null when unset), never as
// timezone-bearing strings, so the frontend can render them in the viewer's own
// timezone.
func TestEpochMSWireFormat(t *testing.T) {
	const ms int64 = 1782560400859

	// A set instant marshals to the bare millisecond number.
	b, err := json.Marshal(MS(time.UnixMilli(ms)))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "1782560400859" {
		t.Errorf("set time = %s, want 1782560400859", got)
	}

	// The zero time marshals to null (e.g. an in-progress recording's endTime).
	if b, _ := json.Marshal(EpochMS{}); string(b) != "null" {
		t.Errorf("zero time = %s, want null", b)
	}

	// A Recording in progress: numeric startTime, null endTime.
	rec := Recording{ID: "r1", StartTime: MS(time.UnixMilli(ms))}
	rb, _ := json.Marshal(rec)
	var raw map[string]any
	if err := json.Unmarshal(rb, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["startTime"] != float64(ms) {
		t.Errorf("startTime = %v (%T), want %d", raw["startTime"], raw["startTime"], ms)
	}
	if raw["endTime"] != nil {
		t.Errorf("endTime = %v, want null", raw["endTime"])
	}
}

// TestEpochMSUnmarshal accepts numbers and null, and tolerates a legacy RFC3339
// string, always recovering the same absolute instant.
func TestEpochMSUnmarshal(t *testing.T) {
	const ms int64 = 1782560400859
	want := time.UnixMilli(ms)

	cases := []struct {
		in   string
		zero bool
		want time.Time
	}{
		{in: "1782560400859", want: want},
		{in: "null", zero: true},
		{in: `"2026-06-27T19:40:00.859+08:00"`, want: want},
	}
	for _, c := range cases {
		var e EpochMS
		if err := json.Unmarshal([]byte(c.in), &e); err != nil {
			t.Fatalf("unmarshal %s: %v", c.in, err)
		}
		if c.zero {
			if !e.IsZero() {
				t.Errorf("unmarshal %s: expected zero, got %v", c.in, e.Time)
			}
			continue
		}
		if !e.Time.Equal(c.want) {
			t.Errorf("unmarshal %s: got %v, want %v", c.in, e.Time.UTC(), c.want.UTC())
		}
	}
}
