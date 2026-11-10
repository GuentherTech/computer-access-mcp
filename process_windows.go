//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// Inno Setup checks this marker before upgrading or uninstalling a running
// agent, including one with its main window closed. Windows releases it on exit.
func registerInstallerMutex() error {
	name, _ := windows.UTF16PtrFromString(`Local\GuentherTech.ComputerAccess`)
	handle, err := windows.CreateMutex(nil, false, name)
	if handle == 0 {
		return err
	}
	return nil
}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Cancel = func() error {
		// Kill descendants too, including native programs started by PowerShell.
		killer := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := killer.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
