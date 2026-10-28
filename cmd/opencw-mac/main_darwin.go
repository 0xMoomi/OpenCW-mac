// Command opencw-mac is the OpenCW root daemon for macOS. launchd starts it
// from /Library/LaunchDaemons/io.github.0xmoomi.opencw.plist; it waits for the
// OpenCW app on a unix socket and, while the app is connected, routes the
// MapleStory Mac client through the fastest relay. When the app goes away the
// network is restored.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"opencw/internal/client"
	"opencw/internal/ipc"
	"opencw/internal/measure"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	// lockPath is a root-only file whose exclusive lock marks a running instance.
	lockPath = "/var/run/opencw.lock"
	// socketPath is where the app connects.
	socketPath = "/var/run/opencw.sock"
	// appPath is the only executable allowed to connect. /Applications/OpenCW.app
	// is installed root-owned, so only an administrator can change it.
	appPath = "/Applications/OpenCW.app/Contents/MacOS/OpenCW"
	// staffGID lets local accounts (group staff) open the socket.
	staffGID = 20
)

// lock holds lockPath's descriptor; the kernel releases the lock on exit.
var lock *os.File

func main() {
	// Running as root, so never resolve helper commands (sing-tun runs
	// dscacheutil by name) from a user-writable PATH directory.
	os.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	// The TUN interface and its routes need root.
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "opencw-mac: must run as root (launchd starts it)")
		os.Exit(1)
	}
	// Two instances would create two utuns with the same address.
	var err error
	lock, err = os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		fmt.Fprintln(os.Stderr, "opencw-mac: already running")
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opencw-mac: lock:", err)
		os.Exit(1)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "opencw-mac:", err)
		os.Exit(1)
	}
}

func run() error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	signal.Ignore(syscall.SIGHUP)
	ln, err := ipc.Listen(socketPath)
	if err != nil {
		return err
	}
	defer os.Remove(socketPath)
	if err := os.Chown(socketPath, 0, staffGID); err != nil {
		ln.Close()
		return err
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		ln.Close()
		return err
	}
	srv := &ipc.Server{
		Allow: ipc.AllowPath(appPath),
		NewController: func(changed func()) ipc.Controller {
			deps := client.DefaultDependencies()
			// The app shows a live ping; the Windows window does not.
			deps.Pinger = func(c client.Connection) client.Pinger { return measure.NewPinger(c.DialContext) }
			return client.New(deps, changed)
		},
		Closed: func(err error) {
			if err != nil {
				fmt.Fprintln(os.Stderr, "opencw-mac: restoring the network failed (a restart restores it):", err)
			}
		},
	}
	fmt.Fprintln(os.Stderr, "opencw-mac", version, "listening on", socketPath)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case <-stop:
		srv.Close() // closes the app's Controller, restoring the network
		return <-served
	case err := <-served:
		srv.Close()
		if err == nil || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}
