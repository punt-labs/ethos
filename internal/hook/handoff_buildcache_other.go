//go:build !linux

package hook

import (
	"context"
	"errors"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

var errHandoffBuildCacheUnsupported = errors.New("snapshot build caches are not supported on this host")

func openHandoffBuildCache(context.Context, layout.Policy, string, string, bool) (snapshotBuildCache, bool, error) {
	return nil, false, errHandoffBuildCacheUnsupported
}

func ClearHandoffBuildCache(context.Context, resolve.Located) error {
	return errHandoffBuildCacheUnsupported
}
