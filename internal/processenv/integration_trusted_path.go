//go:build integrationtest

package processenv

import "os"

const integrationTrustedPathEnv = "AUTO_IMPROVE_INTEGRATION_TRUSTED_PATH"

func init() {
	path := os.Getenv(integrationTrustedPathEnv)
	if path == "" {
		return
	}
	trustedPathState.Lock()
	trustedPathState.value = path
	trustedPathState.Unlock()
}
