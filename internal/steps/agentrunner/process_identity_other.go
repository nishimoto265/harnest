//go:build !darwin && !linux

package agentrunner

import "os/exec"

func platformProcessIdentity(int) (string, error) {
	return "", exec.ErrNotFound
}
