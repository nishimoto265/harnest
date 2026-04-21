package step70_decide

// GitOps abstracts the git interactions that step70 performs against the
// source repository's best_branch. Production wiring provides a real
// implementation backed by `git` subprocesses; tests inject a fake to drive
// deterministic stage transitions.
type GitOps interface {
	// RemoteHead returns the current remote HEAD SHA of branch.
	RemoteHead(branch string) (string, error)
	// PushForceWithLease executes `git push --force-with-lease=<branch>:<expected>`
	// (never plain --force). On lease mismatch (another push won the race), the
	// error returned must unwrap to ErrLeaseFailure so the rollback path can
	// distinguish it from transport errors.
	PushForceWithLease(branch, targetSHA, expected string) error
}

// NoopGitOps is a zero-side-effect implementation used by tests or by the
// stub-only default wiring. RemoteHead always reports best_sha_before so the
// "fresh planning" decision tree treats the branch as untouched.
type NoopGitOps struct{}

func (NoopGitOps) RemoteHead(string) (string, error)                        { return "", nil }
func (NoopGitOps) PushForceWithLease(branch, target, expected string) error { return nil }
