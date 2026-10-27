package ipc

import (
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	procInfoCallPIDInfo = 2    // PROC_INFO_CALL_PIDINFO
	procPIDPathInfo     = 11   // PROC_PIDPATHINFO
	procPIDPathMax      = 4096 // PROC_PIDPATHINFO_MAXSIZE
)

// PeerPath returns the executable of the process at the other end of conn,
// from LOCAL_PEERPID and proc_pidpath.
func PeerPath(conn *net.UnixConn) (string, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return "", err
	}
	var pid int
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		pid, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return "", err
	}
	if sockErr != nil {
		return "", sockErr
	}
	return pidPath(pid)
}

// pidPath is proc_pidpath(3) through the proc_info system call.
func pidPath(pid int) (string, error) {
	buf := make([]byte, procPIDPathMax)
	_, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(pid), procPIDPathInfo, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return "", errno
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), nil
		}
	}
	return string(buf), nil
}

// AllowPath returns a peer check that accepts only processes running the
// executable at path.
func AllowPath(path string) func(*net.UnixConn) bool {
	return func(conn *net.UnixConn) bool {
		got, err := PeerPath(conn)
		return err == nil && got == path
	}
}
