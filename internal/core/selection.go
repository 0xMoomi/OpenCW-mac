package core

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"time"
)

// Measurement is one check of a relay path. Nullable values distinguish
// unmeasured data from a measured zero.
type Measurement struct {
	At                         time.Time
	ExitIP, UDPExitIP, Country string
	// Colo is the IATA code of the Cloudflare data centre that answered the
	// exit (e.g. "LAX"): the nearest large city to the exit, not a geolocation.
	Colo        string
	UDPVerified bool
	// TCPMS and TCPSpreadMS time HTTPS responses after the TLS handshake;
	// IdleMS and SpreadMS are the median and range of the UDP round trips.
	TCPMS, TCPSpreadMS *float64
	IdleMS, SpreadMS   *float64
	Attempts, Failures int
}

// Probe is one candidate with its screen result (Median through Reachable),
// its latest Measurement, and the selection state set by Score and
// CandidateOrder.
type Probe struct {
	Server             Server
	Median, Spread     float64
	Attempts, Failures int
	Reachable          bool
	Measurement        Measurement
	Risk, Uncertainty  float64
	Priority, Band     int
}

const (
	// neutralCost is the cost of a missing or invalid metric.
	neutralCost = .65
	// neutralLoad stands in for relay load, which no feed reports.
	neutralLoad = .5
	// freshFor is how long a measurement describes the current path.
	freshFor = 2 * time.Minute
	// Search-budget hints for NeedsMoreChecks.
	hintLatencyMS = 240
	hintSpreadMS  = 80
)

// Value returns a pointer to v for the nullable Measurement fields.
func Value(v float64) *float64 {
	return &v
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// delay maps a value onto a bounded, diminishing cost in [0, 1).
func delay(v, scale float64) float64 {
	return max(0, v) / (max(0, v) + scale)
}

func metric(v *float64, scale float64) float64 {
	if v == nil || !finite(*v) || *v < 0 {
		return neutralCost
	}
	return delay(*v, scale)
}

func failure(attempts, failures int) float64 {
	// Beta(1,9) prior: a handful of successful attempts is not proof of zero loss.
	return float64(max(0, failures)+1) / float64(max(attempts, failures)+10)
}

// Fresh reports whether m was taken recently enough to describe the path now.
func Fresh(m Measurement, now time.Time) bool {
	return !m.At.IsZero() && !now.Before(m.At) && now.Sub(m.At) <= freshFor
}

// NeedsMoreChecks reports whether m is worth comparing against more
// candidates. These are soft search-budget hints, never eligibility cutoffs:
// a slow or lossy but working exit remains the fallback when no better path
// is found.
func NeedsMoreChecks(m Measurement) bool {
	return m.Failures > 0 || m.IdleMS == nil || !finite(*m.IdleMS) || *m.IdleMS > hintLatencyMS ||
		m.SpreadMS == nil || !finite(*m.SpreadMS) || *m.SpreadMS > hintSpreadMS ||
		(m.TCPMS != nil && (!finite(*m.TCPMS) || *m.TCPMS > hintLatencyMS)) ||
		(m.TCPSpreadMS != nil && (!finite(*m.TCPSpreadMS) || *m.TCPSpreadMS > hintSpreadMS))
}

// ConnectionCost is the game-path cost of a verified connection; lower is better.
func ConnectionCost(m Measurement) float64 {
	// Without a TCP sample the UDP figure stands in; a missing metric is
	// neutral, never zero.
	tcp := m.TCPMS
	if tcp == nil {
		tcp = m.IdleMS
	}
	spread := metric(m.SpreadMS, 80)
	if m.TCPSpreadMS != nil {
		spread = max(spread, metric(m.TCPSpreadMS, 80))
	}
	// Short observed failure fractions need more weight than a long-term prior
	// alone, without a discontinuous "healthy/unhealthy" quality ranking.
	loss := .5*failure(m.Attempts, m.Failures) + .5*float64(max(0, m.Failures))/float64(max(1, m.Attempts))
	return .35*metric(tcp, 300) + .30*metric(m.IdleMS, 300) + .15*spread + .20*loss
}

// Score sets Risk and Uncertainty using bounded, diminishing utilities: the
// verified path cost when a fresh measurement exists, the screen result
// otherwise. Constants are engineering defaults, not thresholds established
// for MapleStory.
func Score(probes []Probe, now time.Time) []Probe {
	result := slices.Clone(probes)
	for i := range result {
		p := &result[i]
		if m := p.Measurement; m.UDPVerified && Fresh(m, now) {
			p.Risk = .95*ConnectionCost(m) + .05*neutralLoad
			p.Uncertainty = .04 + .04*float64(m.Failures)/float64(max(1, m.Attempts))
			continue
		}
		local := neutralCost
		if p.Reachable && finite(p.Median) && !p.Server.IndirectTransport() {
			local = delay(p.Median, 250)
		}
		spread := neutralCost
		if p.Attempts-p.Failures >= 2 {
			spread = delay(p.Spread, 80)
		}
		// No feed advertises throughput; that share stays neutral.
		p.Risk = .45*local + .20*spread + .20*failure(p.Attempts, p.Failures) + .10*neutralCost + .05*neutralLoad
		p.Uncertainty = .025 + .04*float64(p.Failures)/float64(max(1, p.Attempts))
	}
	return result
}

// SmallPoolSize is the largest pool BestGroup keeps whole: small directories
// do not have enough alternatives to justify splitting tiers.
const SmallPoolSize = 5

const (
	// maxGroups bounds the number of quality tiers BestGroup considers.
	maxGroups = 6
	// minNoise floors each probe's Uncertainty, the measurement resolution.
	minNoise = .025
)

// BestGroup returns the best quality tier of probes, one entry per server.
// A small pool is grouped together. Larger pools use minimum within-cluster
// squared error in 1D with a noise-floored, BIC-style penalty.
func BestGroup(probes []Probe) []Probe {
	unique := map[string]Probe{}
	for _, p := range probes {
		key := p.Server.Key()
		if key == "" {
			continue
		}
		if !finite(p.Risk) {
			p.Risk = 1 // Unknown quality changes priority, never server eligibility.
		}
		old, ok := unique[key]
		if !ok || p.Risk < old.Risk {
			unique[key] = p
		}
	}
	all := make([]Probe, 0, len(unique))
	for _, p := range unique {
		all = append(all, p)
	}
	slices.SortFunc(all, func(a, b Probe) int {
		if c := cmp.Compare(a.Risk, b.Risk); c != 0 {
			return c
		}
		return cmp.Compare(a.Server.Key(), b.Server.Key())
	})
	n := len(all)
	if n <= SmallPoolSize || all[0].Risk == all[n-1].Risk {
		return all
	}
	// Centering avoids cancellation when summing squares of nearly equal scores.
	prefix, squares := make([]float64, n+1), make([]float64, n+1)
	noise := make([]float64, n)
	for i, p := range all {
		x := p.Risk - all[0].Risk
		prefix[i+1] = prefix[i] + x
		squares[i+1] = squares[i] + x*x
		noise[i] = max(minNoise, p.Uncertainty)
	}
	slices.Sort(noise)
	resolution := noise[n/2]
	floor := resolution * resolution
	sse := func(a, b int) float64 {
		sum := prefix[b] - prefix[a]
		return max(0, squares[b]-squares[a]-sum*sum/float64(b-a))
	}
	maxK := min(maxGroups, int(math.Ceil(math.Sqrt(float64(n)))))
	dp := make([][]float64, maxK+1)
	cut := make([][]int, maxK+1)
	for k := range dp {
		dp[k] = make([]float64, n+1)
		cut[k] = make([]int, n+1)
		for j := range dp[k] {
			dp[k][j] = math.Inf(1)
		}
	}
	dp[0][0] = 0
	bestK, bestCriterion := 1, math.Inf(1)
	for k := 1; k <= maxK; k++ {
		for end := k; end <= n; end++ {
			for start := k - 1; start < end; start++ {
				cost := dp[k-1][start] + sse(start, end)
				if cost < dp[k][end] {
					dp[k][end] = cost
					cut[k][end] = start
				}
			}
		}
		variance := max(floor, dp[k][n]/float64(n))
		criterion := float64(n)*math.Log(variance) + dp[k][n]/variance + float64(2*k)*math.Log(float64(n))
		if criterion < bestCriterion {
			bestCriterion = criterion
			bestK = k
		}
	}
	end := n
	for k := bestK; k > 1; k-- {
		end = cut[k][end]
	}
	// Merge a boundary which is within the measurement resolution of the best tier.
	for end < n && all[end].Risk-all[end-1].Risk <= resolution && all[end].Risk-all[0].Risk <= 2*resolution {
		end++
	}
	return slices.Clone(all[:end])
}

// History is session-local selection memory.
type History struct {
	// Seed and Sequence make each run's and each selection's draws independent.
	Seed        string
	Sequence    uint64
	FailedUntil map[string]time.Time
	Nodes       map[string]Evidence
	Protocols   map[string]Evidence
}

// Nonce returns 128 random bits as hex.
func Nonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// NewHistory returns an empty History with a fresh seed.
func NewHistory() History {
	return History{
		Seed:        Nonce(),
		FailedUntil: map[string]time.Time{},
		Nodes:       map[string]Evidence{},
		Protocols:   map[string]Evidence{},
	}
}

// Prune drops expired failure marks and evidence.
func (h *History) Prune(now time.Time) {
	for key, t := range h.FailedUntil {
		if !now.Before(t) {
			delete(h.FailedUntil, key)
		}
	}
	for _, records := range []map[string]Evidence{h.Nodes, h.Protocols} {
		for key, e := range records {
			if e.decay(now) == 0 {
				delete(records, key)
			}
		}
	}
}

const (
	// raceScale is the Risk difference that divides a candidate's draw weight by e.
	raceScale = .06
	// maxRaceExponent keeps the weight of a much worse candidate above zero.
	maxRaceExponent = 20.0
)

// Spread orders one probe per server by a quality-weighted random draw, with
// recently failed servers last. The draw is a deterministic function of the
// history seed and sequence, nonce, and server key.
func Spread(pool []Probe, h History, now time.Time, nonce string) []Probe {
	var eligible []Probe
	scores := map[string]float64{}
	minRisk := 1.0
	for _, p := range pool {
		if finite(p.Risk) {
			minRisk = min(minRisk, p.Risk)
		}
	}
	seen := map[string]bool{}
	for _, p := range pool {
		key := p.Server.Key()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		eligible = append(eligible, p)
		sum := sha256.Sum256(fmt.Appendf(nil, "%s:%d:%s:%s", h.Seed, h.Sequence, nonce, key))
		// Exponential race: quality-weighted sampling without replacement.
		// Equal quality spreads evenly; small pools still favor better paths.
		u := (float64(binary.BigEndian.Uint64(sum[:8])>>12) + 1) / (float64(uint64(1)<<52) + 1)
		risk := p.Risk
		if !finite(risk) {
			risk = 1
		}
		weight := math.Exp(-min(maxRaceExponent, max(0, risk-minRisk)/raceScale))
		scores[key] = -math.Log(u) / weight
	}
	slices.SortFunc(eligible, func(a, b Probe) int {
		aFailed := now.Before(h.FailedUntil[a.Server.Key()])
		bFailed := now.Before(h.FailedUntil[b.Server.Key()])
		if aFailed != bFailed {
			if aFailed {
				return 1
			}
			return -1
		}
		return cmp.Compare(scores[a.Server.Key()], scores[b.Server.Key()])
	})
	return eligible
}

// Candidate tiers, best first.
const (
	tierProven      = iota // settled fresh verification, or proven history
	tierReachable          // direct TCP screen succeeded
	tierPreferred          // comes from a preferred feed
	tierOther              // everything else, including failed screens and CDN front doors
	tierCoolingDown        // failed recently
	tierPrevious           // the exit being switched away from
	tierCount
)

// CandidateOrder retains every unique server. Quality, a recent failure, and the
// previous server only change order; none can empty a nonempty directory.
func CandidateOrder(probes []Probe, h History, now time.Time, previous string) []Probe {
	var pools [tierCount][]Probe
	for _, p := range probes {
		p.Risk = h.learnedRisk(p, now)
		tier := tierOther
		if p.Server.Source == sourceAu1rxx || p.Server.Source == source0xRadikal {
			tier = tierPreferred
		}
		if p.Reachable && !p.Server.IndirectTransport() {
			tier = tierReachable
		} else if p.Attempts > 0 {
			tier = tierOther
		}
		if p.Measurement.UDPVerified && Fresh(p.Measurement, now) && !NeedsMoreChecks(p.Measurement) {
			tier = tierProven
		} else if h.Proven(p.Server, now) && !Fresh(p.Measurement, now) {
			tier = tierProven
		}
		if now.Before(h.FailedUntil[p.Server.Key()]) {
			tier = tierCoolingDown
		}
		if previous != "" &&
			(p.Server.Key() == previous || p.Server.IP == previous || p.Measurement.ExitIP == previous) {
			tier = tierPrevious
		}
		p.Priority = tier
		pools[tier] = append(pools[tier], p)
	}
	var result []Probe
	nonce := Nonce()
	for _, remaining := range pools {
		band := 0
		for len(remaining) > 0 {
			group := BestGroup(remaining)
			if len(group) == 0 {
				break
			}
			for i := range group {
				group[i].Band = band
			}
			result = append(result, Spread(group, h, now, nonce)...)
			band++
			used := map[string]bool{}
			for _, p := range group {
				used[p.Server.Key()] = true
			}
			remaining = slices.DeleteFunc(remaining, func(p Probe) bool { return used[p.Server.Key()] })
		}
	}
	return result
}
