package nativeagent

import (
	"golang.org/x/sys/unix"
	"syscall"
)

func writerBootID() (string, error) {
	return unix.Sysctl("kern.bootsessionuuid")
}

func watchWriterParent(_ *syscall.SysProcAttr) {}
