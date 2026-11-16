//go:build alertacceptance

package storage

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"
)

// AlertAcceptanceConfig belongs only to the opt-in acceptance binary. The
// fixture supplies canonical retention authority; PauseFile stops only alert
// evaluation while source ingestion and shared preparation continue normally.
type AlertAcceptanceConfig struct {
	RetentionStart  time.Time
	PauseFile       string
	SourcePauseFile string
}

// The fixture supplies controls to the ordinary processor lifecycle. Its bounds
// and execution are shared with normal deployments; this wrapper adds no engine.
func (r *Runtime) StartAlertAcceptance(ctx context.Context, logger *slog.Logger, cfg AlertAcceptanceConfig) (stop func(), err error) {
	return r.startAlertAcceptance(ctx, logger, cfg, time.Now)
}

func (r *Runtime) startAlertAcceptance(ctx context.Context, logger *slog.Logger, cfg AlertAcceptanceConfig, clock func() time.Time) (func(), error) {
	if ctx == nil || logger == nil || !validMaintenanceTime(cfg.RetentionStart) {
		return nil, ErrAlertInvalid
	}
	evaluationPaused := false
	controls := processingControls{RetentionStart: cfg.RetentionStart,
		PauseSource: alertAcceptanceSourcePause(ctx, logger, cfg.SourcePauseFile),
		PauseEvaluation: func() bool {
			paused, err := alertAcceptancePaused(cfg.PauseFile)
			if err != nil {
				logger.WarnContext(ctx, "alert acceptance evaluation paused", "reason", "pause_file_unreadable")
				return true
			}
			if paused != evaluationPaused {
				message := "alert acceptance evaluator resumed"
				if paused {
					message = "alert acceptance evaluator paused"
				}
				logger.InfoContext(ctx, message)
				evaluationPaused = paused
			}
			return paused
		},
	}
	return r.startProcessing(ctx, logger, true, controls, clock)
}

func alertAcceptancePaused(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return true, err
}

func alertAcceptanceSourcePause(ctx context.Context, logger *slog.Logger, path string) func() bool {
	// The serial normal source loop invokes this at the start of its next turn,
	// after all work from the previous turn has returned and released resources.
	wasPaused := false
	return func() bool {
		paused, err := alertAcceptancePaused(path)
		if err != nil {
			logger.WarnContext(ctx, "alert acceptance source paused", "reason", "pause_file_unreadable")
			return paused
		}
		if paused != wasPaused {
			message := "alert acceptance source worker resumed"
			if paused {
				message = "alert acceptance source worker paused"
			}
			logger.InfoContext(ctx, message)
			wasPaused = paused
		}
		return paused
	}
}
