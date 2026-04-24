//go:build linux

package agentrunner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLinuxProcStatStartTicks_ParsesCommandContainingParen(t *testing.T) {
	fields := []string{"S"}
	for i := 4; i <= 21; i++ {
		fields = append(fields, "1")
	}
	fields = append(fields, "987654321")

	startTicks, err := linuxProcStatStartTicks("1234 (agent)runner) " + strings.Join(fields, " "))
	require.NoError(t, err)
	require.Equal(t, "987654321", startTicks)
}

func TestLinuxProcStatStartTicks_RejectsMalformedStat(t *testing.T) {
	_, err := linuxProcStatStartTicks("1234 agent S 1 2 3")
	require.Error(t, err)
}
