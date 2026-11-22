package hook

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpgradeNoticeUsesSharedDiagnosticLog(t *testing.T) {
	require.Empty(t, upgradedStorageLine(0))
	require.Equal(t, "ethos: upgraded storage (2 items; see ~/.punt-labs/ethos/logs/ethos.log)", upgradedStorageLine(2))
}
