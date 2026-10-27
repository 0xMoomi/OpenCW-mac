// Command opencw-mac is a terminal client for macOS: it connects to the
// fastest relay, routes the MapleStory Mac client through it, and restores the
// network on Ctrl+C.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"opencw/internal/client"
)

func main() {
	// The TUN interface and its routes need root.
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "OpenCW는 네트워크 경로를 바꾸기 때문에 관리자 권한이 필요합니다.\n"+
			"아래처럼 앞에 sudo를 붙여 다시 실행해주세요 (Mac 로그인 암호를 물어봅니다).\n\n  sudo %s\n", os.Args[0])
		os.Exit(1)
	}
	os.Exit(run())
}

func run() int {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	changed := make(chan struct{}, 1)
	c := client.New(client.DefaultDependencies(), func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	fmt.Println("OpenCW 시작 (끝내려면 Ctrl+C)")
	c.Toggle()
	code := 0
	last := ""
wait:
	for {
		select {
		case <-stop:
			break wait
		case <-changed:
			s := c.Snapshot()
			if line := describe(s); line != "" && line != last {
				fmt.Println(line)
				last = line
			}
			if !s.Busy && (s.Status == client.StatusFailed || s.Status == client.StatusCancelled) {
				code = 1
				break wait
			}
		}
	}
	fmt.Println("연결을 해제하는 중...")
	if err := c.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "연결 해제 중 오류가 났습니다. Mac을 재시작하면 네트워크 설정이 복원됩니다:", err)
		return 1
	}
	fmt.Println("연결을 해제했습니다. 네트워크 설정이 원래대로 돌아왔습니다.")
	return code
}

// describe is the status line for s, or "" when there is nothing to say.
func describe(s client.Snapshot) string {
	switch s.Status {
	case client.StatusFetching:
		return "서버 목록을 불러오는 중..."
	case client.StatusScreening:
		return "서버 응답을 확인하는 중..."
	case client.StatusVerifying:
		return "가장 빠른 서버를 찾는 중... (" + s.Detail + ")"
	case client.StatusComparing:
		return "가장 빠른 서버를 고르는 중..."
	case client.StatusActivating:
		return "연결 설정 중... (서버 " + s.Detail + ")"
	case client.StatusConnected:
		return "연결됨: 서버 " + s.Server + ping(s) + "\n이제 메이플스토리를 실행하세요. 끝내려면 이 창에서 Ctrl+C를 누르세요."
	case client.StatusDisconnecting:
		return "기존 연결을 해제하는 중..."
	case client.StatusCancelled:
		return "연결이 취소되었습니다."
	case client.StatusFailed:
		return "연결 실패: " + advice(s.Err) + "\n  (" + s.Error + ")"
	}
	return ""
}

// ping formats the connected exit's measured latency.
func ping(s client.Snapshot) string {
	for _, p := range s.Rows {
		m := p.Measurement
		if m.ExitIP != s.Server {
			continue
		}
		text := ""
		if m.TCPMS != nil {
			text += fmt.Sprintf(", TCP %.0fms", *m.TCPMS)
		}
		if m.IdleMS != nil {
			text += fmt.Sprintf(", UDP %.0fms", *m.IdleMS)
		}
		if text == "" {
			return ""
		}
		return " (핑" + text[1:] + ")"
	}
	return ""
}

func advice(err error) string {
	switch {
	case errors.Is(err, client.ErrInterface):
		return "가상 네트워크(TUN)를 만들지 못했습니다. sudo로 실행했는지 확인하고, 다른 VPN이 켜져 있다면 끈 뒤 다시 실행해주세요."
	case errors.Is(err, client.ErrCleanup):
		return "기존 연결을 해제하지 못했습니다. Mac을 재시작하면 네트워크 설정이 복원됩니다."
	case errors.Is(err, client.ErrCatalog):
		return "서버 목록을 불러오지 못했습니다. 인터넷 연결을 확인해주세요."
	case errors.Is(err, client.ErrNoServer):
		return "사용할 수 있는 서버를 찾지 못했습니다. 잠시 후 다시 실행해주세요."
	}
	return "잠시 후 다시 실행해주세요."
}
