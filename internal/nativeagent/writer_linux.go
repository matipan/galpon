package nativeagent

import (
	"os"
	"strings"
	"syscall"
)

func writerBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data)), err
}

func watchWriterParent(attributes *syscall.SysProcAttr) { attributes.Pdeathsig = syscall.SIGKILL }
