// Package measure checks relay candidates. Screen is a cheap TCP pre-check and
// Verify confirms a relay's exit over HTTPS and STUN. Neither measures link
// capacity, packet loss, or the path to a game server.
package measure

import (
	"context"
	"net"
	"time"
)

// DialFunc dials through a relay.
type DialFunc func(context.Context, string, string) (net.Conn, error)

// millis converts d to fractional milliseconds at microsecond resolution.
func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
