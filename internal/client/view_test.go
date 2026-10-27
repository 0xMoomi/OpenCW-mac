package client

import (
	"fmt"
	"testing"

	"opencw/internal/core"
)

func ms(v float64) *float64 { return &v }

func TestViewStates(t *testing.T) {
	connected := Snapshot{Connected: true, Status: StatusConnected, Server: "1.2.3.4",
		Rows: []core.Probe{{Measurement: core.Measurement{ExitIP: "1.2.3.4", IdleMS: ms(99.6), TCPMS: ms(140)}}}}
	for _, tc := range []struct {
		name              string
		snap              Snapshot
		state, title, sub string
		progress          float64 // -1: nil
		ping, signal      int     // ping -1: nil
		canSwitch, busy   bool
		err               string
	}{
		{"idle", Snapshot{Status: StatusStarting}, ViewOff, "연결 안 됨", "게임을 실행하기 전에 연결해주세요", -1, -1, 0, false, false, ""},
		{"loading", Snapshot{Busy: true, Status: StatusFetching}, ViewOff, "준비 중", "서버 목록 불러오는 중", -1, -1, 0, false, true, ""},
		{"fetching", Snapshot{Busy: true, WantConnect: true, Status: StatusFetching}, ViewConnecting, "연결 중", "서버 목록 불러오는 중", .14, -1, 0, false, true, ""},
		{"verifying", Snapshot{Busy: true, WantConnect: true, Status: StatusVerifying, Detail: "2/4"}, ViewConnecting, "연결 중", "최적 서버 탐색 중", .4 + .45*.5, -1, 0, false, true, ""},
		{"recovering", Snapshot{Busy: true, WantConnect: true, Recovering: true, Status: StatusComparing}, ViewConnecting, "재연결 중", "최적 서버 탐색 중", .88, -1, 0, false, true, ""},
		{"connected", connected, ViewConnected, "연결됨", "게임을 실행해주세요", -1, 100, 3, true, false, ""},
		{"disconnecting", Snapshot{Busy: true, Status: StatusDisconnecting}, ViewOff, "연결 해제 중", "네트워크 설정 복원 중", -1, -1, 0, false, true, ""},
		{"catalog failed", Snapshot{Status: StatusFailed, Error: "x: catalog unavailable", Err: fmt.Errorf("x: %w", ErrCatalog)},
			ViewOff, "서버 목록 불러오기 실패", "인터넷 연결을 확인해주세요", -1, -1, 0, false, false, "x: catalog unavailable"},
		{"tun failed", Snapshot{Status: StatusFailed, Error: "tun", Err: ErrInterface},
			ViewOff, "연결 실패", "PC를 재시작한 후 다시 연결해주세요", -1, -1, 0, false, false, "tun"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v View
			v.Update(tc.snap)
			got := v.State()
			if got.State != tc.state || got.Title != tc.title || got.Subtitle != tc.sub ||
				got.CanSwitch != tc.canSwitch || got.Busy != tc.busy || got.Error != tc.err || got.Signal != tc.signal {
				t.Fatalf("got %+v", got)
			}
			if (tc.progress < 0) != (got.Progress == nil) || got.Progress != nil && *got.Progress != tc.progress {
				t.Fatalf("progress %v, want %v", got.Progress, tc.progress)
			}
			if (tc.ping < 0) != (got.PingMS == nil) || got.PingMS != nil && *got.PingMS != tc.ping {
				t.Fatalf("ping %v, want %v", got.PingMS, tc.ping)
			}
		})
	}
}

func TestViewIntent(t *testing.T) {
	var v View
	v.Update(Snapshot{Connected: true, Status: StatusConnected})
	if !v.Switch() {
		t.Fatal("switch refused while connected")
	}
	v.Update(Snapshot{Busy: true, WantConnect: true, Status: StatusScreening})
	if s := v.State(); s.Title != "서버 변경 중" || s.Subtitle != "서버 응답 확인 중" {
		t.Fatalf("switching: %+v", s)
	}
	if v.Switch() {
		t.Fatal("switch accepted while connecting")
	}
	// Turning the switch off mid-connect reads as a cancel until cleanup ends.
	if !v.Toggle() {
		t.Fatal("toggle refused while connecting")
	}
	v.Update(Snapshot{Busy: true, Status: StatusScreening})
	if s := v.State(); s.State != ViewOff || s.Title != "취소 중" {
		t.Fatalf("cancelling: %+v", s)
	}
	if v.Toggle() {
		t.Fatal("toggle accepted while stopping")
	}
	v.Update(Snapshot{Status: StatusCancelled})
	if s := v.State(); s.Title != "연결 안 됨" || s.Busy {
		t.Fatalf("after cancel: %+v", s)
	}
}

func TestSignal(t *testing.T) {
	for _, tc := range []struct {
		m    core.Measurement
		want int
	}{
		{core.Measurement{IdleMS: ms(100)}, 3},
		{core.Measurement{IdleMS: ms(150)}, 2},
		{core.Measurement{IdleMS: ms(200), SpreadMS: ms(30)}, 1},
		{core.Measurement{IdleMS: ms(300)}, 0},
		{core.Measurement{TCPMS: ms(100), Failures: 1}, 1},
	} {
		v := View{}
		v.Update(Snapshot{Connected: true, Server: "a", Rows: []core.Probe{{Measurement: func() core.Measurement { tc.m.ExitIP = "a"; return tc.m }()}}})
		if got := v.State().Signal; got != tc.want {
			t.Errorf("%+v: signal %d, want %d", tc.m, got, tc.want)
		}
	}
}
