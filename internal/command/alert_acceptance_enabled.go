//go:build alertacceptance

package command

import (
	"context"
	"log/slog"
	"os"

	"github.com/dsolsona/almanac/internal/storage"
)

func startAlertAcceptance(ctx context.Context, runtime *storage.Runtime, logger *slog.Logger) (func(), error) {
	retention, err := alertAcceptanceRetention(os.Getenv("ALMANAC_ALERT_ACCEPTANCE_RETENTION_START"))
	if err != nil {
		return nil, err
	}
	return runtime.StartAlertAcceptance(ctx, logger, storage.AlertAcceptanceConfig{
		RetentionStart: retention,
		PauseFile:      "/run/alert-acceptance/pause",
	})
}
