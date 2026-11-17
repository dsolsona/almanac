//go:build !alertacceptance

package command

import (
	"context"
	"log/slog"

	"github.com/dsolsona/almanac/internal/storage"
)

// Ordinary binaries start the shared processors without fixture controls.
func startBackendProcessing(ctx context.Context, runtime *storage.Runtime, logger *slog.Logger, alerts bool) (func(), error) {
	return runtime.StartProcessing(ctx, logger, alerts)
}
