package client

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strings"

	"opencw/internal/core"
)

// View states for ViewState.State: where the switch sits.
const (
	ViewOff        = "off"        // knob left; Busy may still show work (loading, stopping)
	ViewConnecting = "connecting" // knob right, spinner
	ViewConnected  = "connected"  // knob right, traffic routed
)

// ViewState is what a GUI shows for a Snapshot, worded like the Windows app.
type ViewState struct {
	State    string `json:"state"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	// Progress is the bar target while connecting; nil means no determinate
	// fraction (an indeterminate bar while Busy, no bar otherwise).
	Progress *float64 `json:"progress"`
	// PingMS is the connected path's latency, when measured; PingSource says
	// what it measures:
	//   "game":  the live round trip Mac → relay → the game's data centre
	//            (AWS us-west-2) → back, a median of recent probes;
	//   "relay": Mac → relay exit only, from the connect-time check, shown
	//            while the game-region probe fails;
	//   "":      no ping.
	PingMS     *int   `json:"ping_ms"`
	PingSource string `json:"ping_source"`
	// Signal is the Windows app's lit bar count out of 4: 0 (no reading) or
	// 1 (bad), 2 (fair), 3–4 (good), for the same latency PingMS shows.
	Signal int `json:"signal"`
	// Server is the live exit while connected, nil otherwise.
	Server *ServerInfo `json:"server"`
	// CanToggle is false while the network is being restored, when the
	// switch is disabled and toggle is ignored.
	CanToggle bool   `json:"can_toggle"`
	CanSwitch bool   `json:"can_switch"`
	Busy      bool   `json:"busy"`
	Error     string `json:"error"` // raw failure text, set only after a failure
}

// ServerInfo describes the live exit for people.
type ServerInfo struct {
	// Label is where the exit is, in Korean ("미국 로스앤젤레스"), or the
	// country code when the place is unknown.
	Label    string `json:"label"`
	IP       string `json:"ip"`
	Protocol string `json:"protocol"` // vless, hysteria2, shadowsocks, ...
}

type viewPhase int

const (
	phaseIdle       viewPhase = iota // ready, not connected
	phaseLoading                     // fetching the list
	phaseConnecting                  // searching and verifying a route
	phaseConnected                   // traffic routed
	phaseRecovering                  // replacing a route that failed on its own
	phaseStopping                    // cancelling or disconnecting
	phaseFailed                      // last attempt failed
)

// View turns Snapshots into ViewStates. It remembers what the user asked for
// (a server change, a stop) so the wording follows the user's intent, like the
// Windows window does. It is not safe for concurrent use.
type View struct {
	snap      Snapshot
	switching bool // the running operation is a server change
	stopping  bool // the user turned the switch off; wait for cleanup
	cancelled bool // stopping interrupted a connect rather than a route
}

// Update records the latest Snapshot.
func (v *View) Update(s Snapshot) {
	v.snap = s
	if !s.Busy {
		v.switching = false
		v.stopping = false
	}
}

// Toggle records a click on the switch and reports whether the click is
// accepted; the switch is disabled while the network is being restored.
func (v *View) Toggle() bool {
	switch v.phase() {
	case phaseStopping:
		return false
	case phaseConnecting, phaseRecovering:
		v.stopping, v.cancelled = true, true
	case phaseConnected:
		v.stopping, v.cancelled = true, false
	}
	v.switching = false
	return true
}

// Switch records a click on "서버 변경" and reports whether it is accepted;
// the button exists only while connected.
func (v *View) Switch() bool {
	if v.phase() != phaseConnected {
		return false
	}
	v.switching = true
	return true
}

func (v *View) phase() viewPhase {
	s := v.snap
	switch {
	case s.Connected && !s.Busy:
		return phaseConnected
	case s.Busy && s.WantConnect && s.Recovering:
		return phaseRecovering
	case s.Busy && s.WantConnect:
		return phaseConnecting
	case s.Busy && (v.stopping || s.Status == StatusDisconnecting):
		return phaseStopping
	case s.Busy:
		return phaseLoading
	case s.Error != "":
		return phaseFailed
	}
	return phaseIdle
}

// State returns what to show now.
func (v *View) State() ViewState {
	ph := v.phase()
	out := ViewState{State: ViewOff, Busy: v.snap.Busy, CanToggle: ph != phaseStopping}
	out.Title, out.Subtitle = v.texts(ph)
	switch ph {
	case phaseConnecting, phaseRecovering:
		out.State = ViewConnecting
		_, p := v.progressStage()
		out.Progress = &p
	case phaseConnected:
		out.State = ViewConnected
		out.CanSwitch = true
		m, _ := v.active()
		if p := v.snap.Ping; p != nil && p.OK {
			ms := int(math.Round(p.MS))
			out.PingMS, out.PingSource = &ms, "game"
			out.Signal = bars(&p.MS, p.SpreadMS, p.Failures)
		} else if l := latencyOf(m); l != nil {
			ms := int(math.Round(*l))
			out.PingMS, out.PingSource = &ms, "relay"
			out.Signal = signalBars(m)
		}
		if v.snap.Server != "" {
			out.Server = &ServerInfo{Label: placeLabel(m.Country, m.Colo), IP: v.snap.Server, Protocol: v.snap.Protocol}
		}
	case phaseFailed:
		out.Error = v.snap.Error
	}
	return out
}

func (v *View) texts(ph viewPhase) (string, string) {
	switch ph {
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
	}
	return "연결 안 됨", "게임을 실행하기 전에 연결해주세요"
}

func (v *View) stage() string {
	text, _ := v.progressStage()
	return text
}

// progressStage is the coarse "what is happening" line and a bar target.
func (v *View) progressStage() (string, float64) {
	switch v.snap.Status {
	case StatusDisconnecting:
		return "기존 연결 해제 중", .06
	case StatusScreening:
		return "서버 응답 확인 중", .3
	case StatusVerifying:
		var done, total int
		fmt.Sscanf(v.snap.Detail, "%d/%d", &done, &total)
		if total > 0 {
			return "최적 서버 탐색 중", .4 + .45*float64(done)/float64(total)
		}
		return "최적 서버 탐색 중", .4
	case StatusComparing:
		return "최적 서버 탐색 중", .88
	case StatusActivating, StatusConnected:
		return "연결 설정 중", .96
	}
	return "서버 목록 불러오는 중", .14
}

func (v *View) failure() (string, string) {
	err := v.snap.Err
	lower := strings.ToLower(v.snap.Error)
	denied := errors.Is(err, fs.ErrPermission) ||
		strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "permission denied")
	switch {
	case errors.Is(err, ErrInterface) && denied:
		// The helper runs as root, so a denial means a broken install.
		return "권한 부족", "OpenCW를 다시 설치해주세요"
	case errors.Is(err, ErrInterface):
		return "연결 실패", "Mac을 재시작한 후 다시 연결해주세요"
	case errors.Is(err, ErrCleanup):
		return "연결 해제 실패", "Mac을 재시작하면 네트워크 설정이 복원됩니다"
	case errors.Is(err, ErrCatalog):
		return "서버 목록 불러오기 실패", "인터넷 연결을 확인해주세요"
	}
	return "연결 실패", "잠시 후 다시 연결해주세요"
}

func (v *View) active() (core.Measurement, bool) {
	for _, row := range v.snap.Rows {
		if row.Measurement.ExitIP == v.snap.Server {
			return row.Measurement, true
		}
	}
	return core.Measurement{}, false
}

func latencyOf(m core.Measurement) *float64 {
	if m.IdleMS != nil {
		return m.IdleMS
	}
	return m.TCPMS
}

// signalBars is the Windows app's 0–4 bar verdict for a measurement.
func signalBars(m core.Measurement) int {
	spread := 0.0
	if m.SpreadMS != nil {
		spread = *m.SpreadMS
	}
	return bars(latencyOf(m), spread, m.Failures)
}

// bars is the Windows app's 0–4 bar verdict (0: no reading) with its
// thresholds.
func bars(latency *float64, spread float64, failures int) int {
	switch {
	case latency == nil:
		return 0
	case *latency <= 120 && spread <= 15 && failures == 0:
		return 4
	case *latency <= 180 && spread <= 15 && failures == 0:
		return 3
	case *latency <= 260 && spread <= 40 && failures <= 1:
		return 2
	}
	return 1
}
