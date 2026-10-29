//go:build windows

package lockfile

import (
	"golang.org/x/sys/windows"
)

// processAlive reports whether a Windows process with the given PID exists.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}
