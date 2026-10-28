package measure

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"opencw/internal/core"
)

// ErrExitPolicy marks an exit that requires immediate replacement: changed,
// outside the US, or not public. A timeout at the measurement service can
// instead be confirmed before disrupting a game.
var ErrExitPolicy = errors.New("exit policy")

const (
	verifyBudget      = 8 * time.Second
	verifyStepTimeout = 4 * time.Second
	traceChecks       = 2
	stunChecks        = 3
	stunTimeout       = 1200 * time.Millisecond
	// maxSmallResponse caps the trace and geolocation bodies.
	maxSmallResponse = 4096

	traceURL   = "https://www.cloudflare.com/cdn-cgi/trace"
	stunServer = "stun.cloudflare.com:3478"
)

// STUN (RFC 8489) wire constants.
const (
	stunBindingRequest  = 0x0001
	stunBindingSuccess  = 0x0101
	stunMagicCookie     = 0x2112a442
	stunXORMappedAddr   = 0x0020
	stunFamilyIPv4      = 1
	stunHeaderSize      = 20
	stunTransactionSize = 12
)

// Verify sends only a small HTTPS response and three real UDP STUN bindings.
// These are relay-path measurements, not RTT measurements to a game server.
func Verify(parent context.Context, dial DialFunc) (core.Measurement, error) {
	ctx, cancel := context.WithTimeout(parent, verifyBudget)
	defer cancel()
	m := core.Measurement{At: time.Now()}
	ipv4Dial := ipv4Only(dial)
	tr := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		DialContext:           ipv4Dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   verifyStepTimeout,
		ResponseHeaderTimeout: verifyStepTimeout,
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("unexpected validation redirect")
		},
	}
	// Independent flows can be checked together. Five tiny exchanges do not
	// require five sequential tunnel/TLS handshakes. Join every worker before
	// the caller is allowed to release the engine, including early rejection.
	type response struct {
		udp         bool
		ip, country string
		colo        string
		ms          float64
		err         error
	}
	responses := make(chan response, traceChecks+stunChecks)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	for range traceChecks {
		workers.Go(func() {
			t, err := trace(ctx, hc)
			responses <- response{ip: t.ip, country: t.country, colo: t.colo, ms: t.ms, err: err}
		})
	}
	for range stunChecks {
		workers.Go(func() {
			r := response{udp: true}
			conn, err := ipv4Dial(ctx, "udp", stunServer)
			if err != nil {
				r.err = err
				responses <- r
				return
			}
			unlink := context.AfterFunc(ctx, func() { conn.Close() })
			start := time.Now()
			r.ip, r.err = stunBinding(ctx, conn)
			r.ms = millis(time.Since(start))
			unlink()
			conn.Close()
			responses <- r
		})
	}
	var tcpSamples, samples []float64
	var udpErr error
	for range traceChecks + stunChecks {
		r := <-responses
		if r.udp {
			m.Attempts++
			if r.err != nil {
				m.Failures++
				udpErr = r.err
				continue
			}
			if m.UDPExitIP != "" && m.UDPExitIP != r.ip {
				return m, fmt.Errorf("%w: UDP exit IP changed", ErrExitPolicy)
			}
			m.UDPExitIP = r.ip
			samples = append(samples, r.ms)
			continue
		}
		if r.err != nil {
			return m, fmt.Errorf("HTTPS: %w", r.err)
		}
		if r.ip == "" || len(r.country) != 2 {
			return m, errors.New("exit response missing")
		}
		if m.ExitIP != "" && m.ExitIP != r.ip {
			return m, fmt.Errorf("%w: TCP exit IP changed", ErrExitPolicy)
		}
		m.ExitIP, m.Country = r.ip, r.country
		if m.Colo == "" {
			m.Colo = r.colo
		}
		if m.Country != "US" {
			return m, fmt.Errorf("%w: exit country %s", ErrExitPolicy, m.Country)
		}
		if !core.PublicIPv4(m.ExitIP) {
			return m, fmt.Errorf("%w: exit %s is not a public IPv4 address", ErrExitPolicy, m.ExitIP)
		}
		if r.ms > 0 {
			tcpSamples = append(tcpSamples, r.ms)
		}
	}
	if len(tcpSamples) == 2 {
		m.TCPMS = core.Value((tcpSamples[0] + tcpSamples[1]) / 2)
		m.TCPSpreadMS = core.Value(max(tcpSamples[0], tcpSamples[1]) - min(tcpSamples[0], tcpSamples[1]))
	}
	if len(samples) < 2 {
		return m, fmt.Errorf("udp check: %w", udpErr)
	}
	if m.UDPExitIP != m.ExitIP {
		country, err := exitCountry(ctx, hc, m.UDPExitIP)
		if err != nil {
			return m, fmt.Errorf("udp country lookup: %w", err)
		}
		if country != "US" {
			return m, fmt.Errorf("%w: UDP exit outside US", ErrExitPolicy)
		}
	}
	slices.Sort(samples)
	m.IdleMS = core.Value(samples[len(samples)/2])
	m.SpreadMS = core.Value(samples[len(samples)-1] - samples[0])
	m.UDPVerified = true
	return m, nil
}

// ipv4Only resolves hostnames to a public IPv4 address before dialing, so the
// check uses the IPv4 path the game uses even if the relay prefers IPv6 DNS.
func ipv4Only(dial DialFunc) DialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if !core.PublicIPv4(host) {
			if host, err = core.LookupPublicIPv4(ctx, host); err != nil {
				return nil, err
			}
			if host == "" {
				return nil, errors.New("validation host has no public IPv4")
			}
		}
		return dial(ctx, network, net.JoinHostPort(host, port))
	}
}

// traceResult is one Cloudflare trace: the exit IP, its country, the data
// centre that answered and the response time after the TLS handshake.
type traceResult struct {
	ip, country, colo string
	ms                float64
}

// trace fetches the exit IP and country and times the response after the
// connection and TLS handshake are up.
func trace(ctx context.Context, hc *http.Client) (traceResult, error) {
	// HTTP trace hooks may run concurrently, so retain monotonic times under
	// one mutex.
	var mu sync.Mutex
	var connected, firstByte time.Time
	hooks := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			mu.Lock()
			defer mu.Unlock()
			connected = time.Now()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			defer mu.Unlock()
			firstByte = time.Now()
		},
	}
	ctx = httptrace.WithClientTrace(ctx, hooks)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	if err != nil {
		return traceResult{}, err
	}
	req.Header.Set("Cache-Control", "no-cache, no-store")
	res, err := hc.Do(req)
	if err != nil {
		return traceResult{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxSmallResponse+1))
	if err != nil {
		return traceResult{}, err
	}
	if res.StatusCode != http.StatusOK || len(b) > maxSmallResponse {
		return traceResult{}, errors.New("invalid exit response")
	}
	var t traceResult
	for line := range strings.SplitSeq(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			t.ip = strings.TrimSpace(v)
		case "loc":
			t.country = strings.TrimSpace(v)
		case "colo":
			t.colo = strings.TrimSpace(v)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !connected.IsZero() && firstByte.After(connected) {
		t.ms = millis(firstByte.Sub(connected))
	}
	return t, nil
}

// exitCountry geolocates the UDP exit when it differs from the HTTPS exit.
func exitCountry(ctx context.Context, hc *http.Client, ip string) (string, error) {
	if !core.PublicIPv4(ip) {
		return "", errors.New("invalid UDP exit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwho.is/"+ip+"?fields=success,ip,country_code", nil)
	if err != nil {
		return "", err
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("geolocation unavailable")
	}
	var geo struct {
		Success bool   `json:"success"`
		IP      string `json:"ip"`
		Country string `json:"country_code"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxSmallResponse)).Decode(&geo); err != nil {
		return "", err
	}
	if !geo.Success || geo.IP != ip {
		return "", errors.New("invalid geolocation")
	}
	return geo.Country, nil
}

// stunBinding sends one binding request and returns the mapped address.
func stunBinding(ctx context.Context, conn net.Conn) (string, error) {
	request := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(request, stunBindingRequest)
	binary.BigEndian.PutUint32(request[4:], stunMagicCookie)
	transaction := request[8:stunHeaderSize]
	if _, err := rand.Read(transaction); err != nil {
		return "", err
	}
	deadline := time.Now().Add(stunTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	// QUIC datagram adapters have a read deadline but no write-deadline API.
	// Cancellation in Verify closes the connection if the whole check expires.
	if err := conn.SetReadDeadline(deadline); err != nil {
		return "", err
	}
	if _, err := conn.Write(request); err != nil {
		return "", err
	}
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return "", err
		}
		// Ignore responses to an earlier binding; each request has a fresh nonce.
		if n >= stunHeaderSize && !bytes.Equal(buf[8:stunHeaderSize], transaction) {
			continue
		}
		return parseSTUN(buf[:n], transaction)
	}
}

// parseSTUN validates a binding success response for transaction and returns
// its public XOR-MAPPED-ADDRESS.
func parseSTUN(b, transaction []byte) (string, error) {
	if len(transaction) != stunTransactionSize || len(b) < stunHeaderSize ||
		binary.BigEndian.Uint16(b) != stunBindingSuccess ||
		binary.BigEndian.Uint32(b[4:]) != stunMagicCookie ||
		!bytes.Equal(b[8:stunHeaderSize], transaction) ||
		int(binary.BigEndian.Uint16(b[2:])) != len(b)-stunHeaderSize {
		return "", errors.New("invalid STUN response")
	}
	for i := stunHeaderSize; i+4 <= len(b); {
		typ, n := binary.BigEndian.Uint16(b[i:]), int(binary.BigEndian.Uint16(b[i+2:]))
		i += 4
		if n > len(b)-i {
			return "", errors.New("truncated STUN attribute")
		}
		if typ == stunXORMappedAddr && n == 8 && b[i+1] == stunFamilyIPv4 {
			// The IPv4 address is XORed with the magic cookie (bytes 4-7).
			var ip [4]byte
			for j := range 4 {
				ip[j] = b[i+4+j] ^ b[4+j]
			}
			a := netip.AddrFrom4(ip).String()
			if !core.PublicIPv4(a) {
				return "", errors.New("non-public UDP exit")
			}
			return a, nil
		}
		// Attributes are padded to a 4-byte boundary.
		i += (n + 3) &^ 3
	}
	return "", errors.New("missing STUN mapped address")
}
