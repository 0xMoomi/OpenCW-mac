//go:build darwin

package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"opencw/internal/client"
)

type fakeController struct {
	mu      sync.Mutex
	snap    client.Snapshot
	changed func()
	calls   []string
	closed  chan struct{}
}

func (f *fakeController) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

func (f *fakeController) set(s client.Snapshot) {
	f.mu.Lock()
	f.snap = s
	f.mu.Unlock()
	f.changed()
}

func (f *fakeController) Start() {
	f.record("start")
	f.set(client.Snapshot{Busy: true, Status: client.StatusFetching})
}

func (f *fakeController) Toggle() {
	f.record("toggle")
	f.set(client.Snapshot{Busy: true, WantConnect: true, Status: client.StatusScreening})
}

func (f *fakeController) Switch() { f.record("switch") }

func (f *fakeController) Close() error {
	f.record("close")
	close(f.closed)
	return nil
}

func (f *fakeController) Snapshot() client.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeController) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type harness struct {
	t     *testing.T
	path  string
	srv   *Server
	made  chan *fakeController
	ended chan error
}

func newHarness(t *testing.T, allow func(*net.UnixConn) bool) *harness {
	t.Helper()
	// Socket paths are limited to 104 bytes; t.TempDir can be longer.
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &harness{t: t, path: filepath.Join(dir, "s.sock"), made: make(chan *fakeController, 4), ended: make(chan error, 1)}
	h.srv = &Server{
		Allow: allow,
		NewController: func(changed func()) Controller {
			f := &fakeController{changed: changed, closed: make(chan struct{}), snap: client.Snapshot{Status: client.StatusStarting}}
			h.made <- f
			return f
		},
	}
	// A stale file at the path is replaced.
	if err := os.WriteFile(h.path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(h.path); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v, %v", fi.Mode(), err)
	}
	go func() { h.ended <- h.srv.Serve(ln) }()
	t.Cleanup(func() {
		h.srv.Close()
		if err := <-h.ended; err != nil {
			t.Error("serve:", err)
		}
	})
	return h
}

type peer struct {
	conn net.Conn
	r    *bufio.Reader
}

func (h *harness) dial() *peer {
	h.t.Helper()
	conn, err := net.Dial("unix", h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { conn.Close() })
	return &peer{conn, bufio.NewReader(conn)}
}

func (p *peer) next(t *testing.T) client.ViewState {
	t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := p.r.ReadBytes('\n')
	if err != nil {
		t.Fatal("read:", err)
	}
	var v client.ViewState
	if err := json.Unmarshal(line, &v); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return v
}

// waitFor reads states until one has the title.
func (p *peer) waitFor(t *testing.T, title string) client.ViewState {
	t.Helper()
	for {
		if v := p.next(t); v.Title == title {
			return v
		}
	}
}

func (p *peer) send(t *testing.T, line string) {
	t.Helper()
	if _, err := p.conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
}

func waitClosed(t *testing.T, f *fakeController) {
	t.Helper()
	select {
	case <-f.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("controller not closed")
	}
}

func allowAll(*net.UnixConn) bool { return true }

func TestSession(t *testing.T) {
	h := newHarness(t, allowAll)
	p := h.dial()
	if v := p.next(t); v.State != client.ViewOff || v.Title != "연결 안 됨" {
		t.Fatalf("first state %+v", v)
	}
	f := <-h.made
	if v := p.waitFor(t, "준비 중"); !v.Busy || v.Progress != nil {
		t.Fatalf("loading %+v", v)
	}
	p.send(t, `not json`)
	p.send(t, `{"cmd":"switch"}`) // not connected: ignored like a hidden button
	p.send(t, `{"cmd":"toggle"}`)
	v := p.waitFor(t, "연결 중")
	if v.State != client.ViewConnecting || v.Progress == nil || *v.Progress != .3 {
		t.Fatalf("connecting %+v", v)
	}
	f.set(client.Snapshot{Connected: true, Status: client.StatusConnected})
	if v := p.waitFor(t, "연결됨"); v.State != client.ViewConnected || !v.CanSwitch {
		t.Fatalf("connected %+v", v)
	}
	p.send(t, `{"cmd":"switch"}`)
	p.conn.Close()
	waitClosed(t, f)
	if got := f.Calls(); len(got) != 4 || got[0] != "start" || got[1] != "toggle" || got[2] != "switch" || got[3] != "close" {
		t.Fatalf("calls %v", got)
	}
}

func TestReplace(t *testing.T) {
	h := newHarness(t, allowAll)
	a := h.dial()
	a.next(t)
	first := <-h.made
	b := h.dial()
	b.next(t)
	waitClosed(t, first)
	a.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, err := a.r.ReadBytes('\n'); err != nil {
			break
		}
	}
	second := <-h.made
	h.srv.Close()
	waitClosed(t, second)
}

func TestPeerRejected(t *testing.T) {
	h := newHarness(t, AllowPath("/Applications/OpenCW.app/Contents/MacOS/OpenCW"))
	p := h.dial()
	p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, err := p.r.ReadBytes('\n'); err == nil {
		t.Fatalf("rejected peer got %q", line)
	}
	select {
	case <-h.made:
		t.Fatal("controller made for a rejected peer")
	default:
	}
}

func TestPeerAccepted(t *testing.T) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, AllowPath(exe))
	p := h.dial()
	if v := p.next(t); v.Title != "연결 안 됨" {
		t.Fatalf("first state %+v", v)
	}
}
