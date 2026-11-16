//go:build alertacceptance

package command

import (
	"context"
	"log/slog"
	"os"

	"github.com/dsolsona/almanac/internal/storage"
)

func startBackendProcessing(ctx context.Context, runtime *storage.Runtime, logger *slog.Logger, alerts bool) (func(), error) {
	if !alerts {
		return runtime.StartProcessing(ctx, logger, false)
	}
	retention, err := alertAcceptanceRetention(os.Getenv("ALMANAC_ALERT_ACCEPTANCE_RETENTION_START"))
	if err != nil {
		return nil, err
	}
	return runtime.StartAlertAcceptance(ctx, logger, storage.AlertAcceptanceConfig{
		RetentionStart:  retention,
		PauseFile:       "/run/alert-acceptance/pause",
		SourcePauseFile: "/run/alert-acceptance/pause-source",
	})
}
