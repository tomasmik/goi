package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var (
	// ErrDatabaseInUse indicates that another process holds an incompatible database lock.
	ErrDatabaseInUse = errors.New("database is in use by another process")
	// ErrFileLockInUse indicates that another process holds an exclusive file lock.
	ErrFileLockInUse = errors.New("file lock is already held by another process")
	errLockBusy      = errors.New("lock is already held")
)

type Lock struct {
	file      *os.File
	exclusive bool
	path      string
}

func AcquireLock(databasePath string, exclusive bool) (*Lock, error) {
	lock, err := acquireAdvisoryLock(databasePath+".lock", exclusive)
	if err == nil {
		return lock, nil
	}
	if errors.Is(err, errLockBusy) {
		return nil, fmt.Errorf("%w: %w", ErrDatabaseInUse, err)
	}
	return nil, fmt.Errorf("open database lock: %w", err)
}

// AcquireExclusiveFileLock returns ErrFileLockInUse instead of waiting.
func AcquireExclusiveFileLock(path string) (*Lock, error) {
	lock, err := acquireAdvisoryLock(path, true)
	if err == nil {
		return lock, nil
	}
	if errors.Is(err, errLockBusy) {
		return nil, fmt.Errorf("%w: %w", ErrFileLockInUse, err)
	}
	return nil, fmt.Errorf("open exclusive file lock: %w", err)
}

func acquireBackupOutputLock(outputPath string) (*Lock, error) {
	lock, err := acquireAdvisoryLock(outputPath+".backup.lock", true)
	if err == nil {
		return lock, nil
	}
	if errors.Is(err, errLockBusy) {
		return nil, fmt.Errorf("backup output is in use: %w", err)
	}
	return nil, fmt.Errorf("open backup output lock: %w", err)
}

func acquireAdvisoryLock(path string, exclusive bool) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("%w: %w", errLockBusy, err), closeErr)
		}
		return nil, errors.Join(err, closeErr)
	}
	return &Lock{file: file, exclusive: exclusive, path: filepath.Clean(path)}, nil
}

func (lock *Lock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	file := lock.file
	lock.file = nil
	lock.exclusive = false
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func (lock *Lock) holdsExclusive(path string) bool {
	return lock != nil && lock.file != nil && lock.exclusive && lock.path == filepath.Clean(path)
}
