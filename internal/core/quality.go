package core

import (
	"math"
	"net/netip"
	"slices"
	"time"
)

// Evidence is session-local. A check is an observation, not a statistically
// independent game session. Old evidence loses weight with a ten-minute half-life.
type Evidence struct {
	At               time.Time
	LastOK           bool
	Success, Failure float64
	Mean, Deviation  float64
}

const (
	evidenceLifetime = 30 * time.Minute
	evidenceHalfLife = 10 * time.Minute
	// vlessPreference is the Risk bonus for VLESS, the protocol confirmed to
	// carry the game in testing.
	vlessPreference = .025
)

func (e Evidence) decay(now time.Time) float64 {
	if e.At.IsZero() || now.Before(e.At) || now.Sub(e.At) > evidenceLifetime {
		return 0
	}
	return math.Exp2(-now.Sub(e.At).Seconds() / evidenceHalfLife.Seconds())
}

func (e Evidence) observed(now time.Time, cost float64, success bool) Evidence {
	d := e.decay(now)
	e.Success *= d
	e.Failure *= d
	if success {
		if e.Success < .01 {
			e.Mean, e.Deviation = cost, 0
		} else {
			alpha := 1 / min(4, e.Success+1)
			e.Deviation += alpha * (math.Abs(cost-e.Mean) - e.Deviation)
			e.Mean += alpha * (cost - e.Mean)
		}
		e.Success++
	} else {
		e.Failure++
	}
	e.At = now
	e.LastOK = success
	return e
}

func (e Evidence) estimate(prior float64, now time.Time) float64 {
	d := e.decay(now)
	s, f := e.Success*d, e.Failure*d
	weight := min(.85, s/(s+2))
	return (1-weight)*prior + weight*min(1, e.Mean+e.Deviation) + .20*f/(s+f+2)
}

// Record adds one observation of s. It never receives canceled speculative
// work. Protocol observations come from connection attempts only;
// minute-by-minute checks must not count as new protocol connection successes.
func (h *History) Record(s Server, m Measurement, success, attempt bool, now time.Time) {
	if h.Nodes == nil {
		h.Nodes = map[string]Evidence{}
	}
	if h.Protocols == nil {
		h.Protocols = map[string]Evidence{}
	}
	cost := ConnectionCost(m)
	h.Nodes[s.Key()] = h.Nodes[s.Key()].observed(now, cost, success)
	if attempt {
		h.Protocols[s.Protocol()] = h.Protocols[s.Protocol()].observed(now, cost, success)
	}
}

// Proven reports repeated, recent, good, failure-free evidence for s.
func (h History) Proven(s Server, now time.Time) bool {
	e := h.Nodes[s.Key()]
	return e.decay(now) > .5 && e.LastOK && e.Success >= 1.5 &&
		e.Mean+e.Deviation < .40 && e.Failure < 1
}

func (h History) learnedRisk(p Probe, now time.Time) float64 {
	risk := p.Risk
	e := h.Nodes[p.Server.Key()]
	if p.Measurement.UDPVerified && Fresh(p.Measurement, now) {
		// A good historical average cannot erase today's bad measurement.
		return .80*risk + .20*e.estimate(risk, now) - protocolPreference(p)
	}
	risk = e.estimate(risk, now)
	protocol := h.Protocols[p.Server.Protocol()]
	if d := protocol.decay(now); d > 0 {
		s, f := protocol.Success*d, protocol.Failure*d
		// Weak, bounded prior. An entire protocol is never blacklisted.
		risk += .10 * (.5 - (s+1)/(s+f+2))
	}
	// Prefer the protocol confirmed to carry the game when the proxy
	// measurements are close; a materially faster alternative still wins.
	return risk - protocolPreference(p)
}

func protocolPreference(p Probe) float64 {
	if p.Server.Protocol() == "vless" && (!p.Measurement.UDPVerified || !NeedsMoreChecks(p.Measurement)) {
		return vlessPreference
	}
	return 0
}

// SelectionCost is deliberately dominated by the current TCP/UDP measurement.
func (h History) SelectionCost(p Probe, now time.Time) float64 {
	cost := ConnectionCost(p.Measurement)
	return .80*cost + .20*h.Nodes[p.Server.Key()].estimate(cost, now) - protocolPreference(p)
}

// lowerRisk orders probes by Risk, breaking ties by server key.
func lowerRisk(a, b Probe) bool {
	return a.Risk < b.Risk || (a.Risk == b.Risk && a.Server.Key() < b.Server.Key())
}

// FinalistOrder randomizes only truly comparable, freshly verified paths. The
// latency guard prevents a composite score or missing metrics putting a 300 ms
// path in the same draw as a 150 ms path. VLESS is preferred within that group,
// never at the expense of a much faster path.
func FinalistOrder(pool []Probe, h History, now time.Time) []Probe {
	if len(pool) == 0 {
		return nil
	}
	// Multiple credentials/front doors for the same exit get one lottery entry.
	unique := map[string]Probe{}
	for _, p := range pool {
		key := p.Measurement.ExitIP
		if key == "" {
			key = p.Server.Key()
		}
		if old, ok := unique[key]; !ok || lowerRisk(p, old) {
			unique[key] = p
		}
	}
	pool = make([]Probe, 0, len(unique))
	for _, p := range unique {
		pool = append(pool, p)
	}
	best := pool[0]
	for _, p := range pool[1:] {
		if lowerRisk(p, best) {
			best = p
		}
	}
	near := func(a, b *float64) bool {
		return a == nil || b == nil || *a <= *b*1.15+5
	}
	group := slices.DeleteFunc(slices.Clone(pool), func(p Probe) bool {
		return p.Risk > best.Risk+.025 || p.Measurement.Failures > best.Measurement.Failures ||
			!near(p.Measurement.IdleMS, best.Measurement.IdleMS) || !near(p.Measurement.TCPMS, best.Measurement.TCPMS)
	})
	if slices.ContainsFunc(group, func(p Probe) bool { return p.Server.Protocol() == "vless" }) {
		group = slices.DeleteFunc(group, func(p Probe) bool { return p.Server.Protocol() != "vless" })
	}
	return Spread(group, h, now, "verified-finalists")
}

// PickCandidate spreads simultaneous checks across endpoint networks and
// protocols, but only inside the same evidence/quality band. /24 is a cheap
// diversity hint, not a claim about operator or ASN independence.
func PickCandidate(order []Probe, start int, pending map[string]Server) int {
	first, best := -1, -1
	bestPenalty := math.Inf(1)
	// Queue swaps made for earlier batches can move a formerly blocked peer.
	// Find the best available band again rather than relying on slice position.
	for i := start; i < len(order); i++ {
		p := order[i]
		if _, busy := pending[p.Server.IP]; busy {
			continue
		}
		if first < 0 || p.Priority < order[first].Priority ||
			(p.Priority == order[first].Priority && p.Band < order[first].Band) {
			first = i
		}
	}
	if first < 0 {
		return -1
	}
	for i := start; i < len(order); i++ {
		p := order[i]
		if _, busy := pending[p.Server.IP]; busy {
			continue
		}
		if p.Priority != order[first].Priority || p.Band != order[first].Band {
			continue
		}
		if p.Risk > order[first].Risk+.05 {
			continue
		}
		penalty := 0.0
		for _, active := range pending {
			if endpointNetwork(p.Server) == endpointNetwork(active) {
				penalty += 4
			}
			if p.Server.Protocol() == active.Protocol() {
				penalty++
			}
		}
		if penalty < bestPenalty {
			best, bestPenalty = i, penalty
		}
	}
	if best < 0 {
		return first
	}
	return best
}

// endpointNetwork is the /24 of an IPv4 endpoint, else the address itself.
func endpointNetwork(s Server) string {
	if ip, err := netip.ParseAddr(s.IP); err == nil && ip.Is4() {
		return netip.PrefixFrom(ip, 24).Masked().String()
	}
	return s.IP
}
