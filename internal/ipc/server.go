//go:build darwin

// Package ipc serves the OpenCW app over a unix socket: the app sends
// newline-delimited JSON commands and receives a client.ViewState line on
// connect and after every change.
//
//	app → daemon: {"cmd":"toggle"} | {"cmd":"switch"}
//	daemon → app: client.ViewState
//
// Each app connection gets its own Controller, like a Windows window: it
// starts with a catalog refresh and is closed, restoring the network, when the
// app disconnects. One app is served at a time; a new one replaces the old.
package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"opencw/internal/client"
)

// writeTimeout drops an app that stops reading.
const writeTimeout = 5 * time.Second

// Controller is the part of *client.Controller the server drives.
type Controller interface {
	Start()
	Toggle()
	Switch()
	Close() error
	Snapshot() client.Snapshot
}

// Server accepts app connections. NewController makes the Controller for one
// connection, calling changed after each state change. Allow reports whether
// a peer may connect.
type Server struct {
	NewController func(changed func()) Controller
	Allow         func(*net.UnixConn) bool
	// Closed, if set, is called with the error of each Controller's Close.
	Closed func(error)

	mu      sync.Mutex
	ln      net.Listener
	current *session
	closed  bool
}

// Listen removes a stale socket at path and listens there with mode 0600;
// the caller widens ownership and mode as it needs.
func Listen(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	return ln, err
}

// Serve accepts connections on ln until Close.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ln.Close()
	}
	s.ln = ln
	s.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		uc, ok := conn.(*net.UnixConn)
		if !ok || !s.Allow(uc) {
			conn.Close()
			continue
		}
		s.mu.Lock()
		old := s.current
		s.mu.Unlock()
		if old != nil {
			old.stop()
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return nil
		}
		ss := &session{conn: uc, quit: make(chan struct{}), done: make(chan struct{})}
		s.current = ss
		s.mu.Unlock()
		go s.run(ss)
	}
}

// Close stops accepting and ends the current session, closing its
// Controller.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	ln, cur := s.ln, s.current
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	if cur != nil {
		cur.stop()
	}
}

type session struct {
	conn     *net.UnixConn
	quit     chan struct{} // closed to end the session
	done     chan struct{} // closed once the Controller is closed
	stopOnce sync.Once
}

// stop ends the session and waits for its Controller to close.
func (ss *session) stop() {
	ss.stopOnce.Do(func() { close(ss.quit) })
	<-ss.done
}

func (s *Server) run(ss *session) {
	defer close(ss.done)
	changed := make(chan struct{}, 1)
	ctrl := s.NewController(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	defer func() {
		ss.conn.Close()
		err := ctrl.Close()
		if s.Closed != nil {
			s.Closed(err)
		}
	}()

	cmds := make(chan string)
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		scan := bufio.NewScanner(ss.conn)
		for scan.Scan() {
			var msg struct {
				Cmd string `json:"cmd"`
			}
			if json.Unmarshal(scan.Bytes(), &msg) != nil {
				continue
			}
			select {
			case cmds <- msg.Cmd:
			case <-ss.quit:
				return
			}
		}
	}()

	var view client.View
	var last []byte
	send := func() bool {
		view.Update(ctrl.Snapshot())
		line, _ := json.Marshal(view.State())
		line = append(line, '\n')
		if bytes.Equal(line, last) {
			return true
		}
		last = line
		ss.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		_, err := ss.conn.Write(line)
		return err == nil
	}
	if !send() {
		return
	}
	ctrl.Start()
	for {
		select {
		case <-ss.quit:
			return
		case <-gone:
			return
		case <-changed:
		case cmd := <-cmds:
			view.Update(ctrl.Snapshot())
			switch cmd {
			case "toggle":
				if view.Toggle() {
					ctrl.Toggle()
				}
			case "switch":
				if view.Switch() {
					ctrl.Switch()
				}
			}
		}
		if !send() {
			return
		}
	}
}
