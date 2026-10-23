//go:build !unix

package builtin

import "os/exec"

// killProcessGroupOnCancel is a no-op without Unix process groups; the
// default Cancel (kill the direct child) and cmd.WaitDelay still apply.
func killProcessGroupOnCancel(*exec.Cmd) {}
