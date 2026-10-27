package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsolsona/almanac/internal/agentcmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agentcmd.Run(ctx, os.Args[1:]); err != nil {
		message := "almanac-agent: operation failed"
		switch {
		case errors.Is(err, agentcmd.ErrOwnerConflict):
			message = "almanac-agent: another process owns this credential"
		case errors.Is(err, agentcmd.ErrRevoked):
			message = "almanac-agent: credential revoked"
		case errors.Is(err, agentcmd.ErrCredential):
			message = "almanac-agent: credential rejected"
		case errors.Is(err, agentcmd.ErrIncompatible):
			message = "almanac-agent: backend protocol incompatible"
		}
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
}
