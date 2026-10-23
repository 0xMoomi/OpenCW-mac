package measure

import (
	"context"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"opencw/internal/core"
)

const (
	screenWorkers = 64
	screenBudget  = 2 * time.Second
	screenTimeout = 900 * time.Millisecond
)

// Screen is a cheap scheduling hint, never proof that a VPN works. One short
// TCP connect per advertised endpoint, deduplicated across credentials. UDP-only
// endpoints and failed checks remain candidates for real protocol verification.
// Bounded workers prevent hundreds of engines/handshakes being started at once.
func Screen(parent context.Context, probes []core.Probe) []core.Probe {
	return screen(parent, probes, screenWorkers, screenBudget, tcpConnect)
}

// tcpConnect resolves s and times one TCP connect to its endpoint.
func tcpConnect(ctx context.Context, s core.Server, port int) (core.Server, float64, error) {
	ctx, cancel := context.WithTimeout(ctx, screenTimeout)
	defer cancel()
	resolved, err := core.ResolveServer(ctx, s)
	if err != nil {
		return s, 0, err
	}
	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort(resolved.IP, strconv.Itoa(port)))
	ms := millis(time.Since(start))
	if conn != nil {
		conn.Close()
	}
	return resolved, ms, err
}

type screenCheck func(context.Context, core.Server, int) (core.Server, float64, error)

func screen(parent context.Context, probes []core.Probe, workers int, budget time.Duration, check screenCheck) []core.Probe {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	result := slices.Clone(probes)
	type job struct {
		indices []int
		port    int
	}
	var jobs []job
	byEndpoint := map[string]int{}
	for i, p := range result {
		result[i].Attempts, result[i].Failures = 0, 0
		result[i].Reachable = false
		result[i].Median, result[i].Spread = 0, 0
		// Hysteria2 is UDP-only; a TCP connect says nothing about it.
		port := 0
		if p.Server.Proxy != nil && p.Server.Proxy.Type != "hysteria2" {
			port = int(p.Server.Proxy.Port)
		}
		if port == 0 {
			continue
		}
		key := net.JoinHostPort(p.Server.IP, strconv.Itoa(port))
		if j, ok := byEndpoint[key]; ok {
			jobs[j].indices = append(jobs[j].indices, i)
		} else {
			byEndpoint[key] = len(jobs)
			jobs = append(jobs, job{[]int{i}, port})
		}
	}
	queue := make(chan job, len(jobs))
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	var wg sync.WaitGroup
	for range min(workers, len(jobs)) {
		wg.Go(func() {
			for j := range queue {
				if ctx.Err() != nil {
					return
				}
				s, ms, err := check(ctx, result[j.indices[0]].Server, j.port)
				for _, i := range j.indices {
					p := &result[i]
					p.Attempts, p.Failures = 1, 1
					p.Reachable = err == nil
					if err == nil {
						p.Median, p.Spread, p.Failures = ms, 0, 0
						// Keep each credential's identity and TLS hostname when
						// reusing this endpoint's resolved IP.
						p.Server = p.Server.PinnedTo(s.IP)
					}
				}
			}
		})
	}
	wg.Wait()
	return result
}
