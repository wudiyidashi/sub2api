//go:build !windows

package runner

import "os/exec"

func hideProcessWindow(_ *exec.Cmd) {}
