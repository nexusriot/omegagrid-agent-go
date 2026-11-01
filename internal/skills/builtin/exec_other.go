//go:build !unix

package builtin

import (
	"os/exec"
	"syscall"
)

// killProcessGroupOnCancel is a no-op without Unix process groups; the
// default Cancel (kill the direct child) and cmd.WaitDelay still apply.
func killProcessGroupOnCancel(*exec.Cmd) {}

// enableBroadcast is a no-op where SO_BROADCAST is not set this way.
func enableBroadcast(syscall.RawConn) error { return nil }
