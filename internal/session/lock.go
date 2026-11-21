package session

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// A session lock is a sibling file "<journal>.lock" containing the owner's
// pid. The file is created with O_EXCL and removed on the last Close or on
// Delete. It is not held open, so a crashed process leaves a stale file that
// the next Open takes over once the pid is no longer alive. Callers must
// hold registryMu.

func acquireSessionLock(path string) error {
	lockPath := path + ".lock"
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if !os.IsExist(err) {
				return fmt.Errorf("session: lock: %w", err)
			}
			pid, ok := readLockPID(lockPath)
			if ok && pid == os.Getpid() {
				return nil
			}
			if ok && pidAlive(pid) {
				return ErrSessionLocked
			}
			os.Remove(lockPath)
			continue
		}
		_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
		serr := f.Sync()
		cerr := f.Close()
		if werr != nil || serr != nil || cerr != nil {
			os.Remove(lockPath)
			switch {
			case werr != nil:
				return fmt.Errorf("session: lock: %w", werr)
			case serr != nil:
				return fmt.Errorf("session: lock: %w", serr)
			default:
				return fmt.Errorf("session: lock: %w", cerr)
			}
		}
		pid, ok := readLockPID(lockPath)
		if ok && pid == os.Getpid() {
			return nil
		}
	}
	return ErrSessionLocked
}

// releaseSessionLock removes the lock file unless another live process owns it.
func releaseSessionLock(path string) {
	lockPath := path + ".lock"
	pid, ok := readLockPID(lockPath)
	if ok && pid != os.Getpid() && pidAlive(pid) {
		return
	}
	os.Remove(lockPath)
}

func lockHeldByOther(path string) error {
	pid, ok := readLockPID(path + ".lock")
	if !ok {
		return nil
	}
	if pid != os.Getpid() && pidAlive(pid) {
		return ErrSessionLocked
	}
	return nil
}

func readLockPID(lockPath string) (int, bool) {
	b, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
