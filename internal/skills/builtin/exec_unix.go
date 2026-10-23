//go:build unix

package builtin

import (
	"os/exec"
	"syscall"
)

// killProcessGroupOnCancel runs cmd in its own process group and makes context
// cancellation kill the whole group. exec.CommandContext alone kills only
// `sh`; a pipeline or backgrounded child (`sleep 1000 | cat`) survived the
// timeout and kept the stdout pipe open, so Wait never returned.
func killProcessGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
