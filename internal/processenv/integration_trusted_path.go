//go:build integrationtest

package processenv

import "os"

const integrationTrustedPathEnv = "HARNEST_INTEGRATION_TRUSTED_PATH"

func init() {
	path := os.Getenv(integrationTrustedPathEnv)
	if path == "" {
		return
	}
	trustedPathState.Lock()
	trustedPathState.value = path
	trustedPathState.Unlock()
}
