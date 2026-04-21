//go:build !unix

package step50_implement

import (
	"os"
	"os/exec"
)

func configureCommandProcessGroup(cmd *exec.Cmd) {}

func killCommandProcessGroup(cmd *exec.Cmd) error {
	return nil
}

func signalExitCode(processState *os.ProcessState) (int, bool) {
	return 0, false
}
