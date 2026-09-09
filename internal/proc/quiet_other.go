//go:build !windows

package proc

import "os/exec"

// Quiet is a no-op outside Windows: there is no window to suppress.
func Quiet(cmd *exec.Cmd) {}
