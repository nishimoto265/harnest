package step70_decide

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// RealGitOps executes the production git commands against the source repo.
type RealGitOps struct {
	RepoDir string
	Remote  string
}

func (g RealGitOps) RemoteHead(branch string) (string, error) {
	remote := g.remoteName()
	cmd := exec.Command("git", "-C", g.RepoDir, "ls-remote", "--heads", remote, branch)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func (g RealGitOps) PushForceWithLease(branch, targetSHA, expected string) error {
	remote := g.remoteName()
	refspec := fmt.Sprintf("%s:%s", targetSHA, branch)
	lease := fmt.Sprintf("--force-with-lease=%s:%s", branch, expected)
	cmd := exec.Command("git", "-C", g.RepoDir, "push", remote, refspec, lease)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "stale info") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "non-fast-forward") {
			return fmt.Errorf("%w: %s", ErrLeaseFailure, strings.TrimSpace(msg))
		}
		return err
	}
	return nil
}

func (g RealGitOps) remoteName() string {
	if g.Remote != "" {
		return g.Remote
	}
	return "origin"
}
