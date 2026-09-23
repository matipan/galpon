package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFactoryLockAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.lock")
	if !factoryLockAvailable(path) {
		t.Fatal("new lock must be available")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if factoryLockAvailable(path) {
		t.Fatal("held lock was reported as available")
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if !factoryLockAvailable(path) {
		t.Fatal("released lock must be available")
	}
}
