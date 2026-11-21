//go:build windows

package session

import "syscall"

const processQueryLimitedInformation = 0x1000

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err == nil {
		syscall.CloseHandle(h)
		return true
	}
	// Access denied means the process exists but this user cannot inspect it.
	if errno, ok := err.(syscall.Errno); ok && errno == syscall.ERROR_ACCESS_DENIED {
		return true
	}
	return false
}
