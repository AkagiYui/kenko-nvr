package core

import "context"

// SourceState describes the connection state of a source.
type SourceState string

const (
	// StateIdle means the source has not started or has stopped.
	StateIdle SourceState = "idle"
	// StateConnecting means the source is attempting to connect.
	StateConnecting SourceState = "connecting"
	// StateRunning means the source is connected and producing media.
	StateRunning SourceState = "running"
	// StateError means the last connection attempt failed.
	StateError SourceState = "error"
)

// Source pulls or receives media and publishes it into a Stream.
//
// Run blocks until ctx is cancelled or an unrecoverable error occurs. The
// supervisor (the stream manager) is responsible for restarting a Source after
// a transient failure. On the first successful connection a Source must call
// onReady with the established Stream exactly once; readers attach to that
// Stream. If Run returns before onReady is called, the attempt is treated as a
// failed connection.
type Source interface {
	Run(ctx context.Context, onReady func(*Stream)) error
}
