package measure

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"slices"
	"sync"
	"time"
)

// GameRegionURL is the round-trip target of the live ping.
//
// MapleStory NA's login servers (login.maplestory.nexon.net) are EC2 hosts in
// AWS us-west-2 (Oregon), but they accept only the game's own protocol, and
// Nexon's HTTPS hosts are either latency-routed (config.na.nexon.com answers
// from Seoul to a Korean resolver) or CDN edges near the relay. Timing either
// would not be the path the game takes, and repeated connects to the game's
// login port could look abnormal. So the ping times AWS's public DynamoDB
// health endpoint in the same region: the relay's distance to the data
// centre that hosts the game's login servers, through the same relay session
// the game uses.
const GameRegionURL = "https://dynamodb.us-west-2.amazonaws.com/"

const (
	// PingInterval is the time between probes of a live connection.
	PingInterval = 4 * time.Second
	// pingTimeout bounds one probe, connection setup included.
	pingTimeout = 3 * time.Second
	// pingWindow is how many recent probes the reading summarises.
	pingWindow = 5
	// pingFresh is how many of the latest probes must include a success for
	// the reading to stand; otherwise the caller falls back to relay latency.
	pingFresh = 3
)

// Reading summarises the recent probes of one connection.
type Reading struct {
	// OK is false when none of the latest probes succeeded.
	OK bool
	// MS is the median round trip of the successful probes in the window and
	// SpreadMS their range, without the extremes once there are four.
	MS, SpreadMS float64
	// Failures counts failed probes in the window.
	Failures int
}

// Pinger times one small HTTPS request at a time to GameRegionURL on a single
// kept-alive connection through a relay, like sing-box's URLTest but without
// the TCP and TLS setup: only the time from the request leaving to the first
// response byte counts. That is one round trip Mac → relay → AWS us-west-2 →
// relay → Mac plus about a millisecond of server time. It is not safe for
// concurrent use.
type Pinger struct {
	url       string
	client    *http.Client
	transport *http.Transport
	now       func() time.Time
	window    []float64 // recent results in order; a negative value is a failure
}

// NewPinger returns a Pinger that dials through dial.
func NewPinger(dial DialFunc) *Pinger {
	return newPinger(GameRegionURL, ipv4Only(dial), &tls.Config{MinVersion: tls.VersionTLS12}, time.Now)
}

func newPinger(url string, dial DialFunc, tlsConfig *tls.Config, now func() time.Time) *Pinger {
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         dial,
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 1,
		MaxConnsPerHost:     1,
		IdleConnTimeout:     time.Minute,
	}
	return &Pinger{
		url:       url,
		transport: tr,
		client: &http.Client{
			Transport: tr,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: now,
	}
}

// Ping runs one probe and returns the updated reading.
func (p *Pinger) Ping(ctx context.Context) Reading {
	ms, err := p.probe(ctx)
	if err != nil {
		ms = -1
	}
	p.window = append(p.window, ms)
	if len(p.window) > pingWindow {
		p.window = p.window[len(p.window)-pingWindow:]
	}
	return p.reading()
}

// Close drops the kept-alive connection.
func (p *Pinger) Close() { p.transport.CloseIdleConnections() }

func (p *Pinger) reading() Reading {
	var r Reading
	var ok []float64
	for _, ms := range p.window {
		if ms < 0 {
			r.Failures++
		} else {
			ok = append(ok, ms)
		}
	}
	latest := p.window[max(0, len(p.window)-pingFresh):]
	if !slices.ContainsFunc(latest, func(ms float64) bool { return ms >= 0 }) {
		return r
	}
	slices.Sort(ok)
	r.OK = true
	r.MS = ok[len(ok)/2]
	// One stray spike should not paint the connection bad for a whole
	// window, so with four or more samples the extremes are left out.
	spread := ok
	if len(ok) >= 4 {
		spread = ok[1 : len(ok)-1]
	}
	r.SpreadMS = spread[len(spread)-1] - spread[0]
	return r
}

// probe sends one request and times it from the request write to the first
// response byte. A probe that had to open a new connection still excludes the
// setup, since the request is written only after the TLS handshake.
func (p *Pinger) probe(parent context.Context) (float64, error) {
	ctx, cancel := context.WithTimeout(parent, pingTimeout)
	defer cancel()
	var mu sync.Mutex
	var wrote, first time.Time
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			mu.Lock()
			defer mu.Unlock()
			wrote = p.now()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			defer mu.Unlock()
			first = p.now()
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	res, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	// Drain the small body so the connection can be reused.
	_, err = io.Copy(io.Discard, io.LimitReader(res.Body, maxSmallResponse))
	res.Body.Close()
	if err != nil {
		return 0, err
	}
	mu.Lock()
	defer mu.Unlock()
	if wrote.IsZero() || first.Before(wrote) {
		return 0, errors.New("ping not timed")
	}
	return millis(first.Sub(wrote)), nil
}
