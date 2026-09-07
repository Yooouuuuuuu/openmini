//go:build !windows

package agy

import (
	"os/exec"
	"syscall"
	"time"
)

// procAttr puts agy in its own process group so the whole tree can be stopped.
func procAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// stopTree ends agy and everything it spawned.
func stopTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	syscall.Kill(-pid, syscall.SIGTERM)
	time.AfterFunc(3*time.Second, func() { syscall.Kill(-pid, syscall.SIGKILL) })
}
