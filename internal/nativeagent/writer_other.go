//go:build !linux && !darwin

package nativeagent

import "syscall"

// The saved process-group guard prevents a replacement if the supervisor dies.
func writerBootID() (string, error) { return "", nil }

func watchWriterParent(_ *syscall.SysProcAttr) {}
