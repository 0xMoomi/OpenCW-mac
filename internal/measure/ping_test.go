package measure

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestPingerKeepsConnection times probes with a fake clock and checks that
// they share one kept-alive connection.
func TestPingerKeepsConnection(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write([]byte("healthy"))
	}))
	defer srv.Close()
	var dials atomic.Int32
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	// Each probe reads the clock twice: request written, first byte.
	rtts := []time.Duration{150, 140, 400, 145, 150, 160}
	var calls int
	base := time.Unix(0, 0)
	now := func() time.Time {
		i := calls
		calls++
		if i%2 == 0 {
			return base
		}
		return base.Add(rtts[i/2] * time.Millisecond)
	}
	p := newPinger("http://game.test/", dial, nil, now)
	defer p.Close()
	var r Reading
	for range rtts {
		r = p.Ping(context.Background())
	}
	if requests.Load() != int32(len(rtts)) || dials.Load() != 1 {
		t.Fatalf("%d requests over %d dials, want %d over 1", requests.Load(), dials.Load(), len(rtts))
	}
	// The window holds the last five: 140 400 145 150 160. The spike and
	// the low extreme are left out of the spread.
	if !r.OK || r.MS != 150 || r.SpreadMS != 15 || r.Failures != 0 {
		t.Fatalf("reading %+v", r)
	}
}

func TestPingerFailures(t *testing.T) {
	p := newPinger("http://game.test/", func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("relay down")
	}, nil, time.Now)
	if r := p.Ping(context.Background()); r.OK || r.Failures != 1 {
		t.Fatalf("first failure: %+v", r)
	}
	for _, tc := range []struct {
		window []float64
		want   Reading
	}{
		// One success among the latest three keeps the reading.
		{[]float64{100, 120, -1, -1, 130}, Reading{OK: true, MS: 120, SpreadMS: 30, Failures: 2}},
		{[]float64{100, 110, 130, -1, -1}, Reading{OK: true, MS: 110, SpreadMS: 30, Failures: 2}},
		// Three failures in a row fall back, whatever came before.
		{[]float64{100, 110, -1, -1, -1}, Reading{Failures: 3}},
	} {
		p.window = tc.window
		if got := p.reading(); got != tc.want {
			t.Errorf("%v: %+v, want %+v", tc.window, got, tc.want)
		}
	}
}
