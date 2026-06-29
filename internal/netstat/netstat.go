// Package netstat accounts for the server's network throughput, split into two
// directions defined from the server's point of view:
//
//   - ingress (下行): bytes the server RECEIVES — dominated by camera media
//     pulled in over RTSP/ONVIF/GB28181, plus the small change of client request
//     bodies and WebSocket control frames.
//   - egress (上行): bytes the server SENDS to clients — live video pushed to
//     browsers/players (MSE/FLV/TS/HLS), recording downloads, and the web UI and
//     JSON API responses.
//
// Client-facing traffic is captured by wrapping the HTTP listener's connections
// (see Listener), which transparently counts every byte read from and written to
// every client socket — including connections later hijacked for WebSockets.
// Camera ingest does not flow through that listener (the server dials cameras on
// separate sockets), so it is added explicitly via AddIngress.
package netstat

import (
	"net"
	"sync/atomic"
)

var (
	ingress atomic.Uint64
	egress  atomic.Uint64
)

// AddIngress records n bytes received by the server (e.g. camera media).
func AddIngress(n uint64) { ingress.Add(n) }

// AddEgress records n bytes sent by the server.
func AddEgress(n uint64) { egress.Add(n) }

// Ingress returns the cumulative bytes received by the server.
func Ingress() uint64 { return ingress.Load() }

// Egress returns the cumulative bytes sent by the server.
func Egress() uint64 { return egress.Load() }

// Listener wraps l so that every accepted connection counts the bytes it reads
// (into ingress) and writes (into egress). Pass the result to http.Server.Serve.
func Listener(l net.Listener) net.Listener { return &countingListener{l} }

type countingListener struct{ net.Listener }

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	return &countingConn{Conn: c}, nil
}

// countingConn tallies bytes flowing over a client connection. It stays a plain
// net.Conn so the http server's optional-interface probes (e.g. for keep-alive)
// behave exactly as with the unwrapped connection.
type countingConn struct{ net.Conn }

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		AddIngress(uint64(n))
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		AddEgress(uint64(n))
	}
	return n, err
}
