package crash

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

// MonitorArg is the command-line argument that runs the executable as the
// crash monitor for its parent.
const MonitorArg = "--crash-monitor"

// maxReport bounds the captured crash output.
const maxReport = 2 << 20

// Start uses Go's crash-output monitor pattern. The same EXE reads a private
// pipe without a GUI, network, or disk writes during normal operation. Unlike
// recover in main, this also captures a panic in an engine's own goroutine.
// The returned stop function must only be called on NORMAL exit, not in a
// panicking defer (the runtime prints the fatal stack after defers have run).
func Start() (stop func() error, err error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, MonitorArg)
	cmd.Stdin = read
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err = cmd.Start(); err != nil {
		read.Close()
		write.Close()
		return nil, err
	}
	read.Close()
	err = debug.SetCrashOutput(write, debug.CrashOptions{})
	write.Close() // The runtime owns a duplicate; the child never inherits it.
	if err != nil {
		cmd.Wait()
		return nil, err
	}
	return func() error {
		if err := debug.SetCrashOutput(nil, debug.CrashOptions{}); err != nil {
			return err
		}
		return cmd.Wait()
	}, nil
}

// Capture writes only after a fatal report arrives. Normal EOF creates no file
// or directory. No configuration, credentials, environment dump, or automatic
// upload is included. Truncation is bounded, but we drain to avoid blocking a
// crashing runtime on a full pipe.
func Capture(input io.Reader, directory string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxReport))
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(io.Discard, input); err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	if directory == "" {
		base, e := os.UserCacheDir()
		if e != nil {
			base = os.TempDir()
		}
		directory = filepath.Join(base, "OpenCW", "crashes")
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(directory, "crash-"+time.Now().Format("20060102-150405")+"-*.log")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = f.Write(append([]byte(reportHeader()+"\n"), data...))
	closeErr := f.Close()
	if err != nil {
		return name, err
	}
	return name, closeErr
}

// reportHeader identifies the build without any user or machine details.
func reportHeader() string {
	header := fmt.Sprintf("OpenCW crash\nTime: %s\nRuntime: %s %s/%s\n",
		time.Now().Format(time.RFC3339), runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if exe, e := os.Executable(); e == nil {
		if stat, e := os.Stat(exe); e == nil {
			header += "Build: " + stat.ModTime().UTC().Format(time.RFC3339) + "\n"
		}
	}
	return header
}
