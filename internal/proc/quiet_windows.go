//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// Quiet makes cmd start without a console window. openmini itself has no
// console when it runs in its own window, so without this every console
// program it launches would open a terminal window of its own.
func Quiet(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}
