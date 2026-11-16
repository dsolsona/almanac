//go:build !alertacceptance

package command

import (
	"context"
	"log/slog"

	"github.com/dsolsona/almanac/internal/storage"
)

// Ordinary binaries contain no acceptance workers or activation switch.
func startAlertAcceptance(context.Context, *storage.Runtime, *slog.Logger) (func(), error) {
	return func() {}, nil
}
