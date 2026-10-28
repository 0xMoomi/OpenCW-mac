package client

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"opencw/internal/core"
	"opencw/internal/measure"
)

type fakeConnection struct{}

func (fakeConnection) Close() error     { return nil }
func (fakeConnection) EnableTun() error { return nil }
func (fakeConnection) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}

// fakePinger returns the next scripted reading; the last one repeats.
type fakePinger struct {
	mu       sync.Mutex
	readings []measure.Reading
	closed   bool
}

func (p *fakePinger) Ping(context.Context) measure.Reading {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.readings[0]
	if len(p.readings) > 1 {
		p.readings = p.readings[1:]
	}
	return r
}

func (p *fakePinger) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

// TestControllerPing connects through fakes and checks that the connected
// snapshot already carries the first reading and that later probes update it.
func TestControllerPing(t *testing.T) {
	server := core.Server{IP: "203.0.113.10", Proxy: &core.Proxy{Type: "vless", Server: "203.0.113.10", Port: 443, UUID: "u"}}
	pinger := &fakePinger{readings: []measure.Reading{{OK: true, MS: 150}, {OK: true, MS: 150}, {OK: true, MS: 140}}}
	deps := Dependencies{
		Download: func(context.Context) (core.Directory, error) {
			return core.Directory{Servers: []core.Server{server}}, nil
		},
		Start: func(context.Context, core.Server, bool) (Connection, error) { return fakeConnection{}, nil },
		Verify: func(context.Context, Connection) (core.Measurement, error) {
			return core.Measurement{At: time.Now(), ExitIP: "203.0.113.10", Country: "US", Colo: "SEA",
				UDPVerified: true, IdleMS: ms(90), TCPMS: ms(95)}, nil
		},
		Pinger: func(Connection) Pinger { return pinger },
	}
	changed := make(chan struct{}, 64)
	c := New(deps, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	c.pingInterval = 20 * time.Millisecond
	c.comparisonWindow = 10 * time.Millisecond
	c.uncertainWindow = 10 * time.Millisecond
	c.Toggle()
	wait := func(what string, ok func(Snapshot) bool) Snapshot {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			if s := c.Snapshot(); ok(s) {
				return s
			}
			select {
			case <-changed:
			case <-deadline:
				t.Fatalf("timed out waiting for %s: %+v", what, c.Snapshot())
			}
		}
	}
	s := wait("connected", func(s Snapshot) bool { return s.Connected && !s.Busy })
	if s.Ping == nil || s.Ping.MS != 150 || s.Protocol != "vless" {
		t.Fatalf("connected snapshot: ping %+v protocol %q", s.Ping, s.Protocol)
	}
	wait("live ping", func(s Snapshot) bool { return s.Ping != nil && s.Ping.MS == 140 })
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	pinger.mu.Lock()
	defer pinger.mu.Unlock()
	if !pinger.closed {
		t.Fatal("pinger left open")
	}
	if s := c.Snapshot(); s.Connected {
		t.Fatal("still connected after Close")
	}
}
