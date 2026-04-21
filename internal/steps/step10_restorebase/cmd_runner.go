package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type cmdResult struct {
	stdout []byte
	stderr []byte
}

type cmdRunner func(ctx context.Context, name string, args ...string) (cmdResult, error)

func defaultCmdRunner(ctx context.Context, name string, args ...string) (cmdResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.Output()
	if err == nil {
		return cmdResult{stdout: stdout}, nil
	}

	result := cmdResult{stdout: stdout}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.stderr = exitErr.Stderr
	}
	return result, err
}

func formatCmdError(prefix string, err error, out cmdResult) error {
	detail := strings.TrimSpace(string(out.stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(out.stdout))
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w: %s", prefix, err, detail)
}
