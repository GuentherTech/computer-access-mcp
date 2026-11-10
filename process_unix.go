//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func registerInstallerMutex() error { return nil }

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
