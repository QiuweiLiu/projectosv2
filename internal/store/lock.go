package store

import (
	"errors"
	"os"
	"syscall"
)

// ErrLocked means another controller already owns this state directory.
var ErrLocked = errors.New("state directory is owned by another controller")

// Lock is an exclusive advisory lock on a state directory. It stops two Kernel
// processes from scheduling the same Objective; it is not a substitute for the
// atomic claim inside SQLite.
type Lock struct {
	file *os.File
}

func AcquireLock(path string) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, ErrLocked
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}
