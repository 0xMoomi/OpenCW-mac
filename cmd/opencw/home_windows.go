package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"runtime"
	"strings"
	"time"

	"opencw/internal/client"
	"opencw/internal/core"
)

const (
	homeWidth  = 420
	homeHeight = 520
)

type phase int

const (
	phaseLoading    phase = iota // fetching the list; switching on queues a connect
	phaseIdle                    // ready, not connected
	phaseConnecting              // searching and verifying a route
	phaseConnected               // traffic routed
	phaseRecovering              // replacing a route that failed on its own
	phaseStopping                // cancelling or disconnecting
	phaseFailed                  // last attempt failed
	phaseClosing                 // window is closing
)

// ease moves toward a target at a rate set by the wall clock, so motion
// speed does not depend on how often the window repaints.
type ease struct {
	value, target float64
	last          time.Time
	set           bool
}

// to advances toward target and returns the current value.
func (e *ease) to(target float64, now time.Time, rate float64) float64 {
	if !e.set {
		e.value, e.set = target, true
	}
	e.target = target
	dt := math.Min(.1, now.Sub(e.last).Seconds())
	e.last = now
	e.value += (target - e.value) * (1 - math.Exp(-dt*rate))
	if math.Abs(target-e.value) < .002 {
		e.value = target
	}
	return e.value
}

func (e *ease) settled() bool { return !e.set || e.value == e.target }

// homeView turns controller snapshots into what a player needs: whether the
// accelerator is on, what it is doing, and the ping. Everything else lives on
// the details page.
type homeView struct {
	snap        client.Snapshot
	closing     bool
	switching   bool // the running operation is a server change
	stopping    bool // the user turned the switch off; wait for cleanup
	cancelled   bool // stopping interrupted a connect rather than a route
	details     bool
	last        phase
	since       time.Time // start of the current phase
	connectedAt time.Time
	copiedUntil time.Time
	knob        ease
	lift        ease
	bar         ease
}

func (v *homeView) phase() phase {
	s := v.snap
	switch {
	case v.closing:
		return phaseClosing
	case s.Connected && !s.Busy:
		return phaseConnected
	case s.Busy && s.WantConnect && s.Recovering:
		return phaseRecovering
	case s.Busy && s.WantConnect:
		return phaseConnecting
	case s.Busy && (v.stopping || s.Status == client.StatusDisconnecting):
		return phaseStopping
	case s.Busy:
		return phaseLoading
	case s.Error != "":
		return phaseFailed
	}
	return phaseIdle
}

// update records phase transitions so timers restart cleanly. It reports
// whether a live route was just lost, or its replacement failed, without the
// user asking.
func (v *homeView) update(s client.Snapshot) (dropped bool) {
	before := v.phase()
	v.snap = s
	if !s.Busy {
		v.switching = false
		v.stopping = false
	}
	ph := v.phase()
	if ph != v.last || v.since.IsZero() {
		if ph == phaseConnected && v.last != phaseConnected {
			v.connectedAt = time.Now()
		}
		v.last, v.since = ph, time.Now()
		v.bar = ease{}
	}
	return ph != before && (ph == phaseRecovering || ph == phaseFailed) &&
		(before == phaseConnected || before == phaseRecovering)
}

// toggle records what the user meant before the controller reacts.
func (v *homeView) toggle() {
	switch v.phase() {
	case phaseConnecting, phaseRecovering:
		v.stopping, v.cancelled = true, true
	case phaseConnected:
		v.stopping, v.cancelled = true, false
	}
	v.switching = false
}

func (v *homeView) on() bool {
	switch v.phase() {
	case phaseConnecting, phaseConnected, phaseRecovering:
		return true
	}
	return false
}

// texts returns the headline and the one line under it.
func (v *homeView) texts() (string, string) {
	switch v.phase() {
	case phaseLoading:
		return "준비 중", "서버 목록 불러오는 중"
	case phaseConnecting:
		if v.switching {
			return "서버 변경 중", v.stage()
		}
		return "연결 중", v.stage()
	case phaseConnected:
		return "연결됨", "게임을 실행해주세요"
	case phaseRecovering:
		return "재연결 중", v.stage()
	case phaseStopping:
		if v.cancelled {
			return "취소 중", "네트워크 설정 복원 중"
		}
		return "연결 해제 중", "네트워크 설정 복원 중"
	case phaseFailed:
		return v.failure()
	case phaseClosing:
		return "종료 중", "네트워크 설정 복원 중"
	}
	return "연결 안 됨", "게임을 실행하기 전에 연결해주세요"
}

func (v *homeView) stage() string {
	text, _ := v.progressStage()
	return text
}

// progressStage is the coarse "what is happening" line and a bar target.
func (v *homeView) progressStage() (string, float64) {
	switch v.snap.Status {
	case client.StatusDisconnecting:
		return "기존 연결 해제 중", .06
	case client.StatusScreening:
		return "서버 응답 확인 중", .3
	case client.StatusVerifying:
		if done, total := verified(v.snap.Detail); total > 0 {
			return "최적 서버 탐색 중", .4 + .45*float64(done)/float64(total)
		}
		return "최적 서버 탐색 중", .4
	case client.StatusComparing:
		return "최적 서버 탐색 중", .88
	case client.StatusActivating, client.StatusConnected:
		return "연결 설정 중", .96
	}
	return "서버 목록 불러오는 중", .14
}

// verified parses the controller's "done/total" verification progress.
func verified(detail string) (done, total int) {
	fmt.Sscanf(detail, "%d/%d", &done, &total)
	return done, total
}

func (v *homeView) failure() (string, string) {
	err := v.snap.Err
	denied := errors.Is(err, fs.ErrPermission) || strings.Contains(strings.ToLower(v.snap.Error), "access is denied")
	switch {
	case errors.Is(err, client.ErrInterface) && denied:
		return "권한 부족", "관리자 권한으로 실행해주세요"
	case errors.Is(err, client.ErrInterface):
		return "연결 실패", "PC를 재시작한 후 다시 연결해주세요"
	case errors.Is(err, client.ErrCleanup):
		return "연결 해제 실패", "PC를 재시작하면 네트워크 설정이 복원됩니다"
	case errors.Is(err, client.ErrCatalog):
		return "서버 목록 불러오기 실패", "인터넷 연결을 확인해주세요"
	}
	return "연결 실패", "잠시 후 다시 연결해주세요"
}

func (v *homeView) accessibleName() string {
	head, sub := v.texts()
	name := "OpenCW. " + head + ". " + sub
	if probe, ok := v.active(); ok && v.phase() == phaseConnected {
		if l := latencyOf(probe.Measurement); l != nil {
			name += fmt.Sprintf(". 핑 %.0fms, %s", *l, qualityOf(probe.Measurement).label)
		}
	}
	return name
}

func (v *homeView) active() (core.Probe, bool) {
	for _, row := range v.snap.Rows {
		if row.Measurement.ExitIP == v.snap.Server {
			return row, true
		}
	}
	return core.Probe{}, false
}

type quality struct {
	label string // spoken to screen readers; the screen shows bars
	c     color
	bars  int // 1–4, the in-game ping indicator every player knows
}

func latencyOf(m core.Measurement) *float64 {
	if m.IdleMS != nil {
		return m.IdleMS
	}
	return m.TCPMS
}

func qualityOf(m core.Measurement) quality {
	latency, spread := latencyOf(m), 0.0
	if m.SpreadMS != nil {
		spread = *m.SpreadMS
	}
	switch {
	case latency == nil:
		return quality{"", muted, 0}
	case *latency <= 120 && spread <= 15 && m.Failures == 0:
		return quality{"좋음", good, 4}
	case *latency <= 180 && spread <= 15 && m.Failures == 0:
		return quality{"좋음", good, 3}
	case *latency <= 260 && spread <= 40 && m.Failures <= 1:
		return quality{"보통", fair, 2}
	}
	return quality{"나쁨", bad, 1}
}

func protocolName(p core.Probe) string {
	switch name := p.Server.Protocol(); name {
	case "vless":
		return "VLESS"
	case "hysteria2":
		return "Hysteria2"
	case "shadowsocks":
		return "Shadowsocks"
	default:
		return strings.ToUpper(name)
	}
}

// readout formats an optional millisecond reading for the screen.
func readout(v *float64, unit string) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%s", *v, unit)
}

// logValue formats an optional millisecond reading for the diagnostics.
func logValue(v *float64) string {
	if v == nil {
		return "na"
	}
	return fmt.Sprintf("%.0f", *v)
}

func duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d초", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d분", int(d.Minutes()))
	}
	return fmt.Sprintf("%d시간 %d분", int(d.Hours()), int(d.Minutes())%60)
}

func (v *homeView) diagnostics() string {
	s := v.snap
	var b strings.Builder
	// Same logfmt shape as the controller log, so the whole report greps alike.
	fmt.Fprintf(&b, "app=OpenCW version=%s time=%s os=%s/%s\r\n",
		appVersion, time.Now().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "status=%s detail=%q candidates=%d last_compared=%d last_passed=%d\r\n",
		s.Status, s.Detail, s.Candidates, s.Compared, s.Passed)
	if s.Error != "" {
		fmt.Fprintf(&b, "error=%q\r\n", s.Error)
	}
	if p, ok := v.active(); ok {
		m := p.Measurement
		fmt.Fprintf(&b, "connection exit=%s relay=%s protocol=%s tcp_ms=%s udp_ms=%s jitter_ms=%s loss=%d/%d probe=cloudflare\r\n",
			m.ExitIP, p.Server.IP, p.Server.Protocol(), logValue(m.TCPMS), logValue(m.IdleMS), logValue(m.SpreadMS),
			m.Failures, m.Attempts)
	}
	b.WriteString("\r\n")
	b.WriteString(strings.Join(s.Log, "\r\n"))
	return b.String()
}

// frames is the repaint interval the current frame still needs: spinners
// and bars while working, a short burst while something glides, then zero.
func (v *homeView) frames() time.Duration {
	switch v.phase() {
	case phaseLoading, phaseConnecting, phaseRecovering, phaseStopping, phaseClosing:
		return 33 * time.Millisecond
	}
	if !v.knob.settled() || !v.lift.settled() {
		return 16 * time.Millisecond
	}
	return 0
}

func (v *homeView) paint(p *painter, width, height float64) {
	p.fillRound(rect{0, 0, width, height}, 0, canvasBg)
	if v.details {
		v.paintDetails(p, width, height)
		return
	}
	v.paintHome(p, width, height)
}

func (v *homeView) paintHome(p *painter, width, height float64) {
	ph := v.phase()
	t := p.elapsed()
	now := time.Now()
	head, sub := v.texts()

	// The content group is centred vertically and glides when a state adds or
	// removes a block.
	const toggleH, gapHead, headH, subH = 76.0, 28.0, 40.0, 26.0
	extra := 0.0
	switch ph {
	case phaseLoading, phaseConnecting, phaseRecovering, phaseStopping, phaseClosing:
		extra = 28 + 6
	case phaseConnected:
		extra = 26 + 44 + 22 + 40
	}
	group := toggleH + gapHead + headH + 4 + subH + extra
	top := math.Max(56, (height-group)/2)
	knobTarget := 0.0
	if v.on() {
		knobTarget = 1
	}
	knob := v.knob.to(knobTarget, now, 14)
	y := v.lift.to(top, now, 12)

	cx := width / 2
	busy := ph == phaseConnecting || ph == phaseRecovering || ph == phaseStopping || ph == phaseClosing
	enabled := ph != phaseStopping && ph != phaseClosing
	p.toggle("switch", rect{cx - 72, y, 144, toggleH}, knob, v.on(), busy, enabled, t)
	y += toggleH + gapHead
	p.text(head, fontHeadline, ink, rect{20, y, width - 40, headH}, textCenter)
	y += headH + 4
	subColor := muted
	if ph == phaseFailed {
		subColor = bad
	}
	p.text(sub, fontBody, subColor, rect{20, y, width - 40, subH}, textCenter)
	y += subH

	progressRect := rect{cx - 120, y + 28, 240, 6}
	switch ph {
	case phaseLoading, phaseStopping, phaseClosing:
		p.progress(progressRect, -1, t)
	case phaseConnecting, phaseRecovering:
		_, target := v.progressStage()
		p.progress(progressRect, v.bar.to(target, now, 3), t)
	case phaseConnected:
		v.paintPing(p, cx, y+26)
		p.button("change", rect{cx - 60, y + 26 + 44 + 22, 120, 40}, "서버 변경", true)
	}

	// Details sit behind a corner icon; on the details page the same spot
	// closes them, so a double click can never land on another action.
	p.iconButton("details", width-34, 34, p.infoIcon)
}

// paintPing is the one reading a player checks: signal bars for the verdict
// and the latency beside them, centred as a single unit.
func (v *homeView) paintPing(p *painter, cx, y float64) {
	var m core.Measurement
	if probe, ok := v.active(); ok {
		m = probe.Measurement
	}
	latency, q := readout(latencyOf(m), ""), qualityOf(m)
	const barsW, gap, unitGap = 30.0, 14.0, 5.0
	numW := p.measure(latency, fontPing)
	unitW := p.measure("ms", fontBody)
	x := cx - (barsW+gap+numW+unitGap+unitW)/2
	p.signalBars(rect{x, y + 9, barsW, 26}, q.bars, q.c)
	x += barsW + gap
	p.text(latency, fontPing, ink, rect{x, y, numW + 2, 44}, textLeft)
	p.text("ms", fontBody, muted, rect{x + numW + unitGap, y + 8, unitW + 2, 36}, textLeft)
}

type row struct{ key, value string }

func (v *homeView) rows() []row {
	s := v.snap
	ph := v.phase()
	state, _ := v.texts()
	switch ph {
	case phaseConnected:
		state += " · " + duration(time.Since(v.connectedAt))
	case phaseConnecting, phaseRecovering:
		state += " · " + duration(time.Since(v.since))
	}
	rows := []row{{"상태", state}}
	switch ph {
	case phaseConnecting, phaseRecovering:
		rows = append(rows, row{"진행 단계", v.stage()})
		if done, total := verified(s.Detail); total > 0 {
			rows = append(rows, row{"검사한 서버", fmt.Sprintf("%d / %d", done, total)})
		}
	case phaseFailed:
		rows = append(rows, row{"오류", s.Error})
	}
	if p, ok := v.active(); ok && ph == phaseConnected {
		m := p.Measurement
		return append(rows,
			row{"서버 주소", m.ExitIP},
			row{"프로토콜", protocolName(p)},
			row{"TCP 지연", readout(m.TCPMS, " ms")},
			row{"UDP 지연", readout(m.IdleMS, " ms")},
			row{"지터", readout(m.SpreadMS, " ms")},
			row{"손실", fmt.Sprintf("%d / %d", m.Failures, m.Attempts)},
			row{"버전", appVersion},
		)
	}
	if s.Candidates > 0 {
		rows = append(rows, row{"후보 서버", fmt.Sprintf("%d개", s.Candidates)})
	}
	if s.Compared > 0 {
		rows = append(rows, row{"서버 비교", fmt.Sprintf("%d개 중 %d개 통과", s.Compared, s.Passed)})
	}
	return append(rows, row{"버전", appVersion})
}

func (v *homeView) paintDetails(p *painter, width, height float64) {
	p.text("연결 정보", fontPageHead, ink, rect{28, 18, width - 90, 32}, textLeft)
	p.iconButton("back", width-34, 34, p.closeIcon)

	const rowH, causeH = 40.0, 84.0
	rows := v.rows()
	card := rect{24, 68, width - 48, 0}
	for _, r := range rows {
		if r.key == "오류" {
			card.h += causeH
		} else {
			card.h += rowH
		}
	}
	p.fillRound(card, 12, cardBg)
	p.strokeRound(card.inset(.5), 12, 1, border)
	y := card.y
	for i, r := range rows {
		if i > 0 {
			p.line(point{card.x + 18, y}, point{card.x + card.w - 18, y}, 1, divider)
		}
		if r.key == "오류" {
			p.text(r.key, fontRowKey, muted, rect{card.x + 18, y + 10, 120, 24}, textLeft)
			p.text(r.value, fontSmall, ink, rect{card.x + 18, y + 36, card.w - 36, 40}, textParagraphLeft)
			y += causeH
			continue
		}
		p.text(r.key, fontRowKey, muted, rect{card.x + 18, y, 120, rowH}, textLeft)
		p.text(r.value, fontRowValue, ink, rect{card.x + 130, y, card.w - 148, rowH}, textRight)
		y += rowH
	}
	if v.phase() == phaseConnected {
		p.text("지연 시간은 Cloudflare 기준 측정값입니다", fontSmall, muted, rect{28, y + 10, width - 56, 22}, textLeft)
	}

	label := "진단 정보 복사"
	if time.Now().Before(v.copiedUntil) {
		label = "복사됨"
	}
	p.button("copy", rect{width/2 - 90, height - 68, 180, 44}, label, true)
}
