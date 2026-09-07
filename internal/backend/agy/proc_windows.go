//go:build windows

package agy

import (
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
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	cmd.Process.Kill()
}
