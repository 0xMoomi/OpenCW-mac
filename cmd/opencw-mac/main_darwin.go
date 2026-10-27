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

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// lockPath is a root-only file whose exclusive lock marks a running instance.
const lockPath = "/var/run/opencw.lock"

// lock holds lockPath's descriptor; the kernel releases the lock on exit.
var lock *os.File

func main() {
	// Running as root, so never resolve helper commands (sing-tun runs
	// dscacheutil by name) from a user-writable PATH directory.
	os.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	// The TUN interface and its routes need root.
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "OpenCW는 네트워크 경로를 바꾸기 때문에 관리자 권한이 필요합니다.\n"+
			"아래처럼 앞에 sudo를 붙여 다시 실행해주세요 (Mac 로그인 암호를 물어봅니다).\n\n  sudo %s\n", os.Args[0])
		os.Exit(1)
	}
	// Two instances would create two utuns with the same address.
	var err error
	lock, err = os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		fmt.Fprintln(os.Stderr, "OpenCW가 이미 실행 중입니다. 다른 터미널 창에서 실행 중인 OpenCW를 그대로 쓰거나, 그 창에서 Ctrl+C로 끈 뒤 다시 실행해주세요.")
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "실행 잠금 파일을 만들지 못했습니다:", err)
		os.Exit(1)
	}
	os.Exit(run())
}

func run() int {
	stop := make(chan os.Signal, 1)
	// SIGHUP arrives when the Terminal window is closed.
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	changed := make(chan struct{}, 1)
	c := client.New(client.DefaultDependencies(), func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	fmt.Println("OpenCW " + version + " 시작 (끝내려면 Ctrl+C 또는 이 창 닫기)")
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
