package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"opencw/internal/core"
	"opencw/internal/measure"
	"opencw/internal/tunnel"
)

// Status values are stable keys; the UI owns their wording.
const (
	StatusStarting      = "starting"
	StatusFetching      = "fetching"
	StatusScreening     = "screening"
	StatusVerifying     = "verifying" // Detail: "launched/total"
	StatusComparing     = "comparing"
	StatusActivating    = "activating" // Detail: exit IP
	StatusConnected     = "connected"  // Detail: exit IP
	StatusDisconnecting = "disconnecting"
	StatusReady         = "ready"
	StatusCancelled     = "cancelled"
	StatusFailed        = "failed"
)

// Failure classes for Snapshot.Err; the UI turns them into advice with
// errors.Is instead of matching message text.
var (
	ErrCatalog   = errors.New("catalog unavailable")
	ErrNoServer  = errors.New("no usable server")
	ErrInterface = errors.New("tun interface")
	ErrCleanup   = errors.New("cleanup")
)

const (
	logLimit              = 80              // log lines kept in Snapshot.Log
	selectionTTL          = 2 * time.Minute // a connect within this of a fetch skips the download
	connectTimeout        = 35 * time.Second
	parallelChecks        = 8                // verification attempts in flight
	maxSessions           = 12               // attempts in flight plus verified sessions held
	compareEndpoints      = 12               // endpoints checked before an early accept
	compareExits          = 4                // distinct passing exits seen before an early accept
	reservoirSize         = 4                // held finalists: the quality anchor plus three lottery tickets
	defaultAttemptBudget  = 10 * time.Second // one candidate's start plus verification
	defaultComparison     = 5 * time.Second  // challengers' time after the first verified path
	defaultUncertain      = 9 * time.Second  // challengers' time when that path needs more checks
	failureBackoff        = 5 * time.Minute
	defaultHealthInterval = time.Minute
	defaultHealthRecheck  = 15 * time.Second // delay before confirming a failed health check
	healthFailureLimit    = 2                // consecutive failures that replace the connection
)

// Connection is a relay session. It carries dials in userspace until
// EnableTun routes system traffic through it.
type Connection interface {
	Close() error
	EnableTun() error
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Dependencies are the Controller's network operations.
type Dependencies struct {
	Download func(context.Context) (core.Directory, error)
	// Screen is a cheap reachability pre-check that reorders candidates; nil
	// skips it.
	Screen func(context.Context, []core.Probe) []core.Probe
	// Start opens a session for a server; the bool enables the TUN at once.
	Start func(context.Context, core.Server, bool) (Connection, error)
	// Verify measures a session's exit through the session itself.
	Verify func(context.Context, Connection) (core.Measurement, error)
}

// DefaultDependencies returns the production catalog, screen, tunnel and
// verification implementations.
func DefaultDependencies() Dependencies {
	return Dependencies{
		Download: core.FetchCatalog,
		Screen:   measure.Screen,
		Start: func(ctx context.Context, s core.Server, tun bool) (Connection, error) {
			// A (*Session)(nil) converted to Connection is a non-nil interface;
			// failed or canceled speculative starts must return a true nil.
			conn, err := tunnel.Start(ctx, s, tun)
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
		Verify: func(ctx context.Context, c Connection) (core.Measurement, error) {
			return measure.Verify(ctx, c.DialContext)
		},
	}
}

// Snapshot is the Controller state published to the UI.
type Snapshot struct {
	// Busy is set while an operation runs and Connecting while it connects.
	// WantConnect is the end state the user asked for. Recovering marks an
	// operation the Controller started itself to replace a failed connection.
	Busy, Connecting, Connected, WantConnect, Recovering bool
	// Status is a Status* key qualified by Detail. Server is the exit IP.
	Status, Detail, Server, Error string
	Err                           error        // classifiable form of Error
	Rows                          []core.Probe // verified US exits, one per exit IP
	Candidates                    int          // servers in the current selection
	Compared, Passed              int          // hosts verified / passing in the last selection
	Log                           []string     // recent logfmt lines, oldest first
}

// Controller runs one operation at a time (refresh, connect, switch,
// disconnect) and owns the live connection. Its methods are safe for
// concurrent use.
type Controller struct {
	mu              sync.Mutex
	root            context.Context // parent of all work; canceled by Close
	stop            context.CancelFunc
	operationCancel context.CancelFunc
	deps            Dependencies
	state           Snapshot
	history         core.History
	selected        []core.Probe // current candidate order
	fetchedAt       time.Time
	measurements    map[string]core.Measurement // fresh results by server key
	connection      Connection
	connectionStop  context.CancelFunc // ends the connection's lifetime and monitor
	onChange        func()
	closed          bool
	closeOnce       sync.Once
	closeErr        error
	wg              sync.WaitGroup // operations and monitors

	healthInterval   time.Duration
	healthRecheck    time.Duration
	attemptBudget    time.Duration
	comparisonWindow time.Duration
	uncertainWindow  time.Duration
}

// New returns an idle Controller. changed, if non-nil, is called without the
// Controller's lock after each state change; Snapshot reads the new state, so
// notifications that race each other never deliver an older state last.
func New(deps Dependencies, changed func()) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		root:             ctx,
		stop:             cancel,
		deps:             deps,
		history:          core.NewHistory(),
		measurements:     make(map[string]core.Measurement),
		onChange:         changed,
		state:            Snapshot{Status: StatusStarting},
		healthInterval:   defaultHealthInterval,
		healthRecheck:    defaultHealthRecheck,
		attemptBudget:    defaultAttemptBudget,
		comparisonWindow: defaultComparison,
		uncertainWindow:  defaultUncertain,
	}
}

// Snapshot returns a copy of the current state.
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot()
}

// Start fetches the relay catalog without connecting.
func (c *Controller) Start() { c.begin(operation{refresh: true}) }

// Toggle disconnects when connected and connects otherwise, reusing a
// selection fetched within selectionTTL. During an operation it flips the
// wanted end state instead, canceling a connect in progress.
func (c *Controller) Toggle() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.state.Busy {
		c.state.WantConnect = !c.state.WantConnect
		if c.state.Connecting && !c.state.WantConnect && c.operationCancel != nil {
			c.operationCancel()
		}
		c.mu.Unlock()
		c.emit()
		return
	}
	active := c.connection != nil
	fresh := !c.fetchedAt.IsZero() && time.Since(c.fetchedAt) < selectionTTL && len(c.selected) > 0
	c.mu.Unlock()
	if active {
		c.begin(operation{})
		return
	}
	c.begin(operation{refresh: !fresh, connect: true})
}

// Switch refetches the catalog and, when connected, reconnects, preferring an
// exit other than the current one.
func (c *Controller) Switch() { c.switchExit(false) }

// switchExit starts a Switch. recovering marks one the monitor starts to
// replace a failed connection.
func (c *Controller) switchExit(recovering bool) {
	c.mu.Lock()
	op := operation{refresh: true, connect: c.connection != nil, recovering: recovering, exclude: c.state.Server}
	c.mu.Unlock()
	c.begin(op)
}

// Close stops all work, waits for it and closes the connection. Later calls
// return the first call's result.
func (c *Controller) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.close() })
	return c.closeErr
}

func (c *Controller) close() error {
	c.mu.Lock()
	c.closed = true
	c.stop()
	if c.operationCancel != nil {
		c.operationCancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
	c.mu.Lock()
	connection := c.connection
	c.connection = nil
	c.connectionStop = nil
	c.state.Connected = false
	c.state.WantConnect = false
	c.mu.Unlock()
	if connection != nil {
		return connection.Close()
	}
	return nil
}

func (c *Controller) snapshot() Snapshot {
	s := c.state
	s.Rows = slices.Clone(s.Rows)
	s.Log = slices.Clone(s.Log)
	return s
}

func (c *Controller) emit() {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed && c.onChange != nil {
		c.onChange()
	}
}

func (c *Controller) progress(status, detail string) {
	c.mu.Lock()
	changed := c.state.Status != status || c.state.Detail != detail
	c.state.Status = status
	c.state.Detail = detail
	c.mu.Unlock()
	if changed {
		c.emit()
	}
}

// log appends one logfmt line: RFC 3339 time, level, event, key=value pairs.
func (c *Controller) log(level, event string, kv ...any) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-5s %s", time.Now().Format("2006-01-02T15:04:05.000Z07:00"), level, event)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%s", kv[i], logValue(kv[i+1]))
	}
	c.mu.Lock()
	c.state.Log = append(c.state.Log, b.String())
	if len(c.state.Log) > logLimit {
		c.state.Log = c.state.Log[len(c.state.Log)-logLimit:]
	}
	c.mu.Unlock()
}

// logValue renders one logfmt value, quoting only when needed.
func logValue(v any) string {
	var s string
	switch v := v.(type) {
	case *float64:
		if v == nil {
			return "na"
		}
		s = strconv.FormatFloat(*v, 'f', 0, 64)
	case time.Duration:
		s = v.Round(time.Millisecond).String()
	default:
		s = fmt.Sprint(v)
	}
	if s == "" || strings.ContainsFunc(s, needsQuote) {
		return strconv.Quote(s)
	}
	return s
}

// needsQuote reports a rune that a bare logfmt value cannot hold.
func needsQuote(r rune) bool {
	return r == ' ' || r == '"' || r == '=' || unicode.IsControl(r)
}

// operation is one unit of Controller work.
type operation struct {
	refresh    bool   // refetch the catalog first
	connect    bool   // the wanted end state; Toggle may flip it meanwhile
	recovering bool   // started by the monitor to replace a failed connection
	exclude    string // exit to avoid
}

// begin starts op unless an operation is already running.
func (c *Controller) begin(op operation) {
	c.mu.Lock()
	if c.closed || c.state.Busy {
		c.mu.Unlock()
		return
	}
	c.state.Busy = true
	c.state.WantConnect = op.connect
	c.state.Recovering = op.recovering
	c.state.Error = ""
	c.state.Err = nil
	c.state.Connecting = false
	ctx, cancel := context.WithCancel(c.root)
	c.operationCancel = cancel
	c.wg.Add(1)
	c.mu.Unlock()
	c.emit()
	go func() {
		defer c.wg.Done()
		defer cancel()
		c.finish(c.run(ctx, op))
	}()
}

func (c *Controller) finish(err error) {
	canceled := errors.Is(err, context.Canceled)
	c.mu.Lock()
	c.state.Busy = false
	c.state.Connecting = false
	c.state.Recovering = false
	c.operationCancel = nil
	if err != nil {
		c.state.WantConnect = false
		c.state.Connected = false
		c.state.Detail = ""
		if canceled {
			c.state.Status = StatusCancelled
		} else {
			c.state.Status = StatusFailed
			c.state.Error = err.Error()
			c.state.Err = err
		}
	}
	c.mu.Unlock()
	if err != nil && !canceled {
		c.log("ERROR", "operation.failed", "error", err)
	}
	c.emit()
}

func (c *Controller) run(ctx context.Context, op operation) error {
	if err := c.disconnect(); err != nil {
		return err
	}
	if op.refresh {
		if err := c.refresh(ctx, op.exclude); err != nil {
			return err
		}
	}
	c.mu.Lock()
	want := c.state.WantConnect
	c.mu.Unlock()
	if !want {
		c.progress(StatusReady, "")
		return nil
	}
	return c.connect(ctx, op.exclude)
}

func (c *Controller) disconnect() error {
	c.mu.Lock()
	old, oldStop := c.connection, c.connectionStop
	c.connection, c.connectionStop = nil, nil
	c.state.Connected = false
	c.mu.Unlock()
	if oldStop != nil {
		oldStop()
	}
	if old == nil {
		return nil
	}
	c.progress(StatusDisconnecting, "")
	if err := old.Close(); err != nil {
		return fmt.Errorf("%w: %w", ErrCleanup, err)
	}
	return nil
}

// refresh downloads the catalog and rebuilds the selection. The previous
// selection is dropped first so a failed download never leaves it in use.
func (c *Controller) refresh(ctx context.Context, exclude string) error {
	c.mu.Lock()
	c.selected = nil
	c.fetchedAt = time.Time{}
	c.state.Rows = nil
	c.state.Candidates = 0
	c.state.Server = ""
	c.history.Sequence++
	c.history.Prune(time.Now())
	c.mu.Unlock()
	c.progress(StatusFetching, "")
	directory, err := c.deps.Download(ctx)
	for _, source := range directory.Sources {
		if source.Err != nil {
			c.log("WARN", "catalog.source", "name", source.Name, "error", source.Err)
		} else {
			c.log("INFO", "catalog.source", "name", source.Name, "candidates", source.Candidates)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCatalog, err)
	}
	if len(directory.Servers) == 0 {
		return fmt.Errorf("%w: no candidates", ErrCatalog)
	}
	c.log("INFO", "catalog.merged", "candidates", len(directory.Servers),
		"duplicates", directory.Duplicates, "rejected", directory.Rejected)
	c.mu.Lock()
	c.selectLocked(directory.Servers, exclude)
	c.mu.Unlock()
	c.emit()
	return nil
}

// selectLocked orders servers into the selection, reusing fresh measurements
// and forgetting stale ones. c.mu must be held.
func (c *Controller) selectLocked(servers []core.Server, exclude string) {
	now := time.Now()
	for key, m := range c.measurements {
		if !core.Fresh(m, now) {
			delete(c.measurements, key)
		}
	}
	var probes []core.Probe
	for _, server := range servers {
		probes = append(probes, core.Probe{Server: server, Measurement: c.measurements[server.Key()]})
	}
	c.selected = core.CandidateOrder(core.Score(probes, now), c.history, now, exclude)
	c.state.Candidates = len(c.selected)
	c.fetchedAt = now
	c.updateRowsLocked()
}

// updateRowsLocked exposes only locally verified US TCP+UDP exits as usable
// rows. Raw candidates stay in the selection and remain eligible if statistics
// are missing. c.mu must be held.
func (c *Controller) updateRowsLocked() {
	c.state.Rows = nil
	seen := map[string]bool{}
	for _, p := range c.selected {
		m := p.Measurement
		if !m.UDPVerified || m.Country != "US" || !core.Fresh(m, time.Now()) || seen[m.ExitIP] {
			continue
		}
		seen[m.ExitIP] = true
		c.state.Rows = append(c.state.Rows, p)
	}
}

// attemptResult is the outcome of one verification attempt. Its session stays
// open until closeResult releases it or it becomes the live connection.
type attemptResult struct {
	p          core.Probe
	connection Connection
	lifetime   context.Context
	stop       context.CancelFunc
	err        error
}

// remember records a finished check; attempt is false for health checks of
// the live connection. Completed observations survive selection of another
// winner. Canceled work is deliberately not evidence of a bad relay or
// protocol.
func (c *Controller) remember(r *attemptResult, attempt bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	key := r.p.Server.Key()
	c.history.Record(r.p.Server, r.p.Measurement, r.err == nil, attempt, now)
	if r.err == nil {
		c.measurements[key] = r.p.Measurement
		delete(c.history.FailedUntil, key)
		r.p.Risk = c.history.SelectionCost(r.p, now)
	} else {
		c.history.FailedUntil[key] = now.Add(failureBackoff)
		delete(c.measurements, key)
		r.p.Measurement = core.Measurement{}
	}
	for i := range c.selected {
		if c.selected[i].Server.Key() == key {
			c.selected[i].Measurement = r.p.Measurement
		}
	}
	c.updateRowsLocked()
}

// connect screens and verifies candidates, hands the TUN to the best verified
// session and starts its health monitor.
func (c *Controller) connect(parent context.Context, exclude string) error {
	ctx, cancel := context.WithTimeout(parent, connectTimeout)
	defer cancel()
	c.mu.Lock()
	order := slices.Clone(c.selected)
	c.state.Connecting = true
	c.mu.Unlock()
	c.emit()
	if c.deps.Screen != nil {
		order = c.screen(ctx, order)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	order = c.reselect(order, exclude)
	c.emit()
	s := c.newSearch(parent, ctx, cancel, order, exclude)
	defer s.closeInFlight()
	defer s.closeHeld()
	return s.run()
}

func (c *Controller) screen(ctx context.Context, order []core.Probe) []core.Probe {
	c.progress(StatusScreening, "")
	started := time.Now()
	order = c.deps.Screen(ctx, order)
	checked, reachable := 0, 0
	for _, p := range order {
		if p.Attempts > 0 {
			checked++
		}
		if p.Reachable {
			reachable++
		}
	}
	c.log("INFO", "screen.done", "checked", checked, "reachable", reachable, "elapsed", time.Since(started))
	return order
}

// reselect reorders the selection with the latest evidence. Reconnects inside
// the list's freshness window still use new measurements and failures; a list
// download is not required to recompute selection.
func (c *Controller) reselect(order []core.Probe, exclude string) []core.Probe {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.history.Sequence++
	c.history.Prune(now)
	c.selected = core.CandidateOrder(core.Score(order, now), c.history, now, exclude)
	return slices.Clone(c.selected)
}

// search verifies candidates concurrently with real userspace sessions. No OS
// routes are changed by these checks: only the winning session receives the
// TUN, and its authenticated outbound stays alive. All fields belong to the
// connect goroutine; attempt goroutines only send on results.
type search struct {
	c       *Controller
	parent  context.Context // the operation; its cancellation aborts activation
	ctx     context.Context // bounded verification window
	cancel  context.CancelFunc
	exclude string // exit being switched away from; usable only as a fallback
	started time.Time

	order   []core.Probe
	next    int                    // order[:next] has been launched or skipped
	active  int                    // attempts in flight
	pending map[string]core.Server // endpoints with an attempt in flight
	results chan attemptResult

	best     *attemptResult  // quality anchor of the reservoir
	peers    []attemptResult // the reservoir's lottery tickets
	fallback *attemptResult  // best result on the excluded exit
	lastErr  error

	comparison      *time.Timer
	comparisonDone  <-chan time.Time
	comparisonLimit time.Time

	verifiedExits    map[string]bool
	checkedEndpoints map[string]bool
	// An exit-policy failure (wrong country, unverifiable exit) belongs to the
	// host's egress, not to one credential; other credentials on that host
	// would fail the same way and only waste a verification slot.
	deniedHosts  map[string]bool
	targetChecks int
	targetExits  int

	cleanupElapsed time.Duration
}

func (c *Controller) newSearch(parent, ctx context.Context, cancel context.CancelFunc,
	order []core.Probe, exclude string) *search {
	endpoints := map[string]bool{}
	for _, p := range order {
		endpoints[p.Server.IP] = true
	}
	return &search{
		c:                c,
		parent:           parent,
		ctx:              ctx,
		cancel:           cancel,
		exclude:          exclude,
		started:          time.Now(),
		order:            order,
		pending:          map[string]core.Server{},
		results:          make(chan attemptResult, parallelChecks),
		verifiedExits:    map[string]bool{},
		checkedEndpoints: map[string]bool{},
		deniedHosts:      map[string]bool{},
		// Compare a broad set, not merely the first two responders. A bounded
		// deadline still guarantees sparse/dead lists can use the working fallback.
		targetChecks: min(compareEndpoints, len(endpoints)),
		targetExits:  min(compareExits, len(endpoints)),
	}
}

// run launches attempts and handles their results until it accepts a winner
// or runs out of candidates.
func (s *search) run() error {
	for s.next < len(s.order) || s.active > 0 {
		if s.satisfied() {
			return s.accept()
		}
		s.launchBatch()
		if s.active == 0 {
			if s.best != nil {
				return s.accept()
			}
			break
		}
		if s.best == nil {
			s.c.progress(StatusVerifying, fmt.Sprintf("%d/%d", s.next, len(s.order)))
		} else {
			s.c.progress(StatusComparing, "")
		}
		select {
		case <-s.comparisonDone:
			return s.accept()
		case <-s.ctx.Done():
			if err := s.parent.Err(); err != nil {
				return err
			}
			if s.best != nil || s.fallback != nil {
				return s.accept()
			}
			return fmt.Errorf("%w: verification timed out", ErrNoServer)
		case r := <-s.results:
			s.handle(r)
		}
	}
	if s.best != nil || s.fallback != nil {
		return s.accept()
	}
	if s.lastErr == nil {
		return ErrNoServer
	}
	return fmt.Errorf("%w: %w", ErrNoServer, s.lastErr)
}

// satisfied reports whether the anchor needs no challengers and enough
// endpoints and exits have been compared to accept early.
func (s *search) satisfied() bool {
	return s.best != nil && !core.NeedsMoreChecks(s.best.p.Measurement) &&
		len(s.checkedEndpoints) >= s.targetChecks && len(s.verifiedExits) >= s.targetExits
}

// held counts verified sessions kept open for the final choice.
func (s *search) held() int {
	n := len(s.peers)
	if s.best != nil {
		n++
	}
	if s.fallback != nil {
		n++
	}
	return n
}

// launchBatch starts attempts within the session budget while the comparison
// window is open.
func (s *search) launchBatch() {
	capacity := min(parallelChecks, maxSessions-s.held())
	for s.next < len(s.order) && s.active < capacity && s.ctx.Err() == nil &&
		(s.comparison == nil || time.Now().Before(s.comparisonLimit)) {
		// Prefer different relay endpoints within a batch. Alternative
		// credentials/protocols on the same address remain in the queue.
		index := core.PickCandidate(s.order, s.next, s.pending)
		if index < 0 {
			return
		}
		s.order[s.next], s.order[index] = s.order[index], s.order[s.next]
		if !s.deniedHosts[s.order[s.next].Server.IP] {
			s.launch(s.order[s.next])
		}
		s.next++
	}
}

func (s *search) launch(p core.Probe) {
	s.active++
	s.pending[p.Server.IP] = p.Server
	c, ctx, results := s.c, s.ctx, s.results
	go func() { results <- c.attempt(ctx, p) }()
}

func (s *search) handle(r attemptResult) {
	s.active--
	delete(s.pending, r.p.Server.IP)
	s.checkedEndpoints[r.p.Server.IP] = true
	if r.err != nil {
		s.lastErr = r.err
		s.closeResult(r)
		if errors.Is(r.err, measure.ErrExitPolicy) {
			s.deniedHosts[r.p.Server.IP] = true
		}
		if s.ctx.Err() == nil {
			s.c.log("WARN", "verify.failed", "relay", r.p.Server.IP, "protocol", r.p.Server.Protocol(), "error", r.err)
			s.c.remember(&r, true)
		}
		return
	}
	s.c.remember(&r, true)
	s.collect(r)
	if s.best != nil && s.comparison == nil {
		s.openComparison()
	}
}

// openComparison bounds how long challengers may still beat the first
// verified path.
func (s *search) openComparison() {
	window := s.c.comparisonWindow
	if core.NeedsMoreChecks(s.best.p.Measurement) {
		window = s.c.uncertainWindow
	}
	s.comparisonLimit = time.Now().Add(window)
	s.comparison = time.NewTimer(window)
	s.comparisonDone = s.comparison.C
}

// collect adds a verified result to the reservoir. A result on the excluded
// exit only competes for the fallback slot.
func (s *search) collect(r attemptResult) {
	if s.exclude != "" && r.p.Measurement.ExitIP == s.exclude {
		s.keepFallback(r)
		return
	}
	s.verifiedExits[r.p.Measurement.ExitIP] = true
	pool := append(slices.Clone(s.peers), r)
	if s.best != nil {
		pool = append(pool, *s.best)
	}
	ranked := s.c.finalistOrder(pool)
	// Retain the quality anchor plus the best three lottery tickets. This
	// bounded reservoir spreads equivalent exits without retaining engines
	// for the whole directory. Ticket order is stable within this operation.
	anchor := ranked[0]
	for _, p := range ranked[1:] {
		if p.Risk < anchor.Risk {
			anchor = p
		}
	}
	keep := map[string]bool{anchor.Server.Key(): true}
	for _, p := range ranked {
		if len(keep) >= reservoirSize {
			break
		}
		keep[p.Server.Key()] = true
	}
	s.best, s.peers = nil, nil
	for _, candidate := range pool {
		key := candidate.p.Server.Key()
		switch {
		case !keep[key]:
			s.closeResult(candidate)
		case key == anchor.Server.Key():
			s.best = &candidate
		default:
			s.peers = append(s.peers, candidate)
		}
	}
}

// keepFallback holds the lowest-risk result on the excluded exit.
func (s *search) keepFallback(r attemptResult) {
	switch {
	case s.fallback == nil:
		s.fallback = &r
	case r.p.Risk < s.fallback.p.Risk:
		s.closeResult(*s.fallback)
		s.fallback = &r
	default:
		s.closeResult(r)
	}
}

func (s *search) accept() error {
	// Finish all speculative sessions before changing Windows interfaces.
	// Their network monitors must not observe the winner's route transition.
	s.cancel()
	for s.active > 0 {
		loser := <-s.results
		s.active--
		if loser.err != nil {
			s.closeResult(loser)
			continue
		}
		s.c.remember(&loser, true)
		s.checkedEndpoints[loser.p.Server.IP] = true
		s.collect(loser)
	}
	return s.activate(s.choose())
}

// choose draws the winner from the reservoir, or takes the fallback when the
// reservoir is empty, and closes every other held session.
func (s *search) choose() attemptResult {
	var r attemptResult
	if s.best != nil {
		pool := append(slices.Clone(s.peers), *s.best)
		chosen := s.c.finalistOrder(pool)[0].Server.Key()
		for _, candidate := range pool {
			if candidate.p.Server.Key() == chosen {
				r = candidate
			} else {
				s.closeResult(candidate)
			}
		}
		s.best, s.peers = nil, nil
	} else if s.fallback != nil {
		r = *s.fallback
		s.fallback = nil
	}
	if s.fallback != nil {
		s.closeResult(*s.fallback)
		s.fallback = nil
	}
	return r
}

// activate hands the TUN to r and publishes it as the live connection.
func (s *search) activate(r attemptResult) error {
	c := s.c
	if err := s.parent.Err(); err != nil {
		s.closeResult(r)
		return err
	}
	c.progress(StatusActivating, r.p.Measurement.ExitIP)
	if err := r.connection.EnableTun(); err != nil {
		s.closeResult(r)
		// This is a local interface/permission problem, not a failed relay.
		return fmt.Errorf("%w: %w", ErrInterface, err)
	}
	c.mu.Lock()
	// Toggle or Close may have run while EnableTun was blocked.
	if !c.state.WantConnect || c.closed || s.parent.Err() != nil {
		c.mu.Unlock()
		s.closeResult(r)
		return context.Canceled
	}
	c.connection = r.connection
	c.connectionStop = r.stop
	c.state.Connected = true
	c.state.Server = r.p.Measurement.ExitIP
	c.state.Compared, c.state.Passed = len(s.checkedEndpoints), len(s.verifiedExits)
	c.mu.Unlock()
	c.progress(StatusConnected, r.p.Measurement.ExitIP)
	c.log("INFO", "select.done", "compared", len(s.checkedEndpoints), "exits", len(s.verifiedExits),
		"elapsed", time.Since(s.started), "cleanup", s.cleanupElapsed)
	m := r.p.Measurement
	c.log("INFO", "connect.ok", "exit", m.ExitIP, "relay", r.p.Server.IP, "protocol", r.p.Server.Protocol(),
		"tcp_ms", m.TCPMS, "udp_ms", m.IdleMS, "loss", fmt.Sprintf("%d/%d", m.Failures, m.Attempts))
	c.wg.Add(1)
	go c.monitor(r)
	return nil
}

func (s *search) closeResult(r attemptResult) {
	started := time.Now()
	r.stop()
	if r.connection != nil {
		r.connection.Close()
	}
	s.cleanupElapsed += time.Since(started)
}

// closeHeld releases the sessions an unsuccessful search still holds.
func (s *search) closeHeld() {
	if s.comparison != nil {
		s.comparison.Stop()
	}
	if s.best != nil {
		s.closeResult(*s.best)
	}
	if s.fallback != nil {
		s.closeResult(*s.fallback)
	}
	for _, p := range s.peers {
		s.closeResult(p)
	}
}

func (s *search) closeInFlight() {
	s.cancel()
	for s.active > 0 {
		r := <-s.results
		s.active--
		s.closeResult(r)
	}
}

func (c *Controller) finalistOrder(pool []attemptResult) []core.Probe {
	probes := make([]core.Probe, len(pool))
	for i := range pool {
		probes[i] = pool[i].p
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return core.FinalistOrder(probes, c.history, time.Now())
}

// attempt opens a userspace session for p and verifies its exit. The session
// lives on c.root rather than ctx so a winner can outlive the search; while
// the attempt runs, ctx and the attempt budget still cancel it.
func (c *Controller) attempt(ctx context.Context, p core.Probe) attemptResult {
	lifetime, stop := context.WithCancel(c.root)
	unlink := context.AfterFunc(ctx, stop)
	timer := time.AfterFunc(c.attemptBudget, stop)
	conn, err := c.deps.Start(lifetime, p.Server, false)
	if err == nil {
		p.Measurement, err = c.deps.Verify(lifetime, conn)
	}
	unlink()
	timedOut := !timer.Stop()
	// Report the cause, not the cancellation it triggered: the budget and the
	// operation both end the attempt by cancelling lifetime.
	switch {
	case ctx.Err() != nil:
		stop()
		err = ctx.Err()
	case timedOut:
		stop()
		err = context.DeadlineExceeded
	case lifetime.Err() != nil:
		err = lifetime.Err()
	}
	m := p.Measurement
	if err == nil && (!m.UDPVerified || m.Country != "US" || m.ExitIP == "") {
		err = errors.New("exit check incomplete")
	}
	return attemptResult{p: p, connection: conn, lifetime: lifetime, stop: stop, err: err}
}

// monitor re-verifies the live connection every healthInterval and replaces it
// once a failure is confirmed. It exits when the connection's lifetime ends or
// another operation takes over.
func (c *Controller) monitor(live attemptResult) {
	defer c.wg.Done()
	defer live.stop()
	health := time.NewTimer(c.healthInterval)
	defer health.Stop()
	failures := 0
	for {
		select {
		case <-live.lifetime.Done():
			return
		case <-health.C:
		}
		check, current := c.checkHealth(live)
		if !current {
			return
		}
		if check.err == nil {
			failures = 0
			c.remember(&check, false)
			health.Reset(c.healthInterval)
			c.emit()
			continue
		}
		failures++
		if failures < healthFailureLimit && !errors.Is(check.err, measure.ErrExitPolicy) {
			// An isolated probe-service timeout must not tear down an
			// otherwise working game connection or poison its history.
			c.log("WARN", "health.recheck", "error", check.err)
			health.Reset(c.healthRecheck)
			continue
		}
		c.remember(&check, false)
		c.log("ERROR", "health.failed", "error", check.err)
		c.switchExit(true)
		return
	}
}

// checkHealth re-verifies the live connection. current is false when the
// connection was replaced, an operation began or the Controller closed
// meanwhile.
func (c *Controller) checkHealth(live attemptResult) (check attemptResult, current bool) {
	check.p.Server = live.p.Server
	check.p.Measurement, check.err = c.deps.Verify(live.lifetime, live.connection)
	if check.err == nil && check.p.Measurement.ExitIP != live.p.Measurement.ExitIP {
		check.err = fmt.Errorf("%w: exit IP changed", measure.ErrExitPolicy)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return check, c.connection == live.connection && !c.state.Busy && !c.closed
}
