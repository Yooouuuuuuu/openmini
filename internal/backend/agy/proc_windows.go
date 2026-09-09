//go:build windows

package agy

import (
	"openmini/internal/proc"

	"os/exec"
	"strconv"
	"syscall"
)

// procAttr starts agy in a new process group.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// stopTree ends agy and everything it spawned (taskkill /T walks the tree).
func stopTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	proc.Quiet(kill)
	kill.Run()
	cmd.Process.Kill()
}
