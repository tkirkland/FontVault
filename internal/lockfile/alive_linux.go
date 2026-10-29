//go:build linux

package lockfile

import "syscall"

// processAlive reports whether a Linux process with the given PID exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
