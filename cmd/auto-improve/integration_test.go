package main

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	integrationEnvVar            = "AUTO_IMPROVE_INTEGRATION"
	integrationTrustedPathEnvVar = "AUTO_IMPROVE_INTEGRATION_TRUSTED_PATH"
	testTrustedPathSuffix        = "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"
)

func TestIntegrationConcurrentRunsDifferentPRsSucceed(t *testing.T) {
	requireIntegrationEnv(t)

	env := newCLIIntegrationEnv(t, 1*time.Second)
	bin := buildIntegrationBinary(t)

	cmd41, stdout41, stderr41 := env.newRunCommand(bin, 41)
	cmd42, stdout42, stderr42 := env.newRunCommand(bin, 42)

	require.NoError(t, cmd41.Start())
	require.NoError(t, cmd42.Start())

	require.NoError(t, cmd41.Wait(), "stdout=%s stderr=%s", stdout41.String(), stderr41.String())
	require.NoError(t, cmd42.Wait(), "stdout=%s stderr=%s", stdout42.String(), stderr42.String())

	runDirs := env.runDirs(t)
	require.Len(t, runDirs, 2)
	for _, runDir := range runDirs {
		assert.FileExists(t, filepath.Join(runDir, "70", "decision.json"))
	}
	assert.NoDirExists(t, filepath.Join(env.runsBase, "needs-recovery"))
}

func TestIntegrationConcurrentSamePRSecondFailsWithPRLock(t *testing.T) {
	requireIntegrationEnv(t)

	env := newCLIIntegrationEnv(t, 5*time.Second)
	bin := buildIntegrationBinary(t)

	cmd1, stdout1, stderr1 := env.newRunCommand(bin, 42)
	require.NoError(t, cmd1.Start())
	t.Cleanup(func() {
		if cmd1.ProcessState == nil && cmd1.Process != nil {
			_ = cmd1.Process.Kill()
		}
	})

	waitForPath(t, filepath.Join(env.runsBase, "pr-locks", "pr-42.lock"), 5*time.Second)

	cmd2, stdout2, stderr2 := env.newRunCommand(bin, 42)
	err := cmd2.Run()
	require.Error(t, err)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Contains(t, stdout2.String()+stderr2.String(), "another process is already running this PR")

	require.NoError(t, cmd1.Wait(), "stdout=%s stderr=%s", stdout1.String(), stderr1.String())
	assert.NotEmpty(t, stdout2.String()+stderr2.String())
}
