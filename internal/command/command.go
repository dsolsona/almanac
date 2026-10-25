package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsolsona/almanac/internal/config"
	"github.com/dsolsona/almanac/internal/uihttp"
)

type BuildInfo struct {
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	Date          string `json:"date"`
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, info BuildInfo) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return 0
	}
	switch args[0] {
	case "version":
		if len(args) == 2 && args[1] == "--output=json" {
			info.SchemaVersion = 1
			if err := json.MarshalWrite(stdout, info); err != nil {
				fmt.Fprintln(stderr, "version: output failed")
				return 1
			}
			fmt.Fprintln(stdout)
			return 0
		}
		if len(args) > 1 {
			fmt.Fprintln(stderr, "version: usage: almanac version [--output=json]")
			return 2
		}
		fmt.Fprintf(stdout, "almanac %s (commit %s, built %s)\n", info.Version, info.Commit, info.Date)
		return 0
	case "serve":
		return serve(ctx, args[1:], stdout, stderr)
	case "db":
		return db(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "almanac: unsupported command %q\n", args[0])
		return 2
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  almanac version [--output=json]
  almanac serve [--config=FILE] [--components=LIST] [--listen=ADDR] [--backend-url=ORIGIN] [--shutdown-timeout=DURATION]
  almanac db upgrade [--database-url-file=FILE] [--timeout=30s] [--output=json]

Implemented component modes:
  ui       Serve the embedded web UI and proxy /api/v1/ to --backend-url.

Unsupported modes:
  backend, alerts, combined backend/ui/alerts are not implemented yet and fail before listening.`)
}

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "strict JSON configuration file")
	fs.String("listen", "", "HTTP listen address")
	fs.String("components", "", "comma-separated components")
	fs.String("backend-url", "", "fixed backend origin for UI mode")
	fs.String("shutdown-timeout", "", "graceful shutdown deadline")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "serve: unexpected positional argument")
		return 2
	}
	explicit := make(map[string]string)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = f.Value.String() })
	cfg, err := config.Load(*configPath, os.Environ(), explicit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !cfg.UIOnly() {
		fmt.Fprintf(stderr, "serve: components %q are not implemented; only --components=ui is supported; no listener was opened\n", cfg.Components)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	server, err := uihttp.New(cfg, logger)
	if err != nil {
		fmt.Fprintln(stderr, "serve: configuration is invalid")
		return 2
	}
	return runServer(ctx, server, cfg, logger, stderr)
}

func runServer(ctx context.Context, server *uihttp.Server, cfg config.Config, logger *slog.Logger, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("listener failed", "address", cfg.Listen)
		return 1
	}
	errch := make(chan error, 1)
	go func() { errch <- server.Serve(listener) }()
	logger.Info("component started", "component", "ui", "address", listener.Addr().String())
	select {
	case err := <-errch:
		if errors.Is(err, uihttp.ErrServerClosed) {
			return 0
		}
		logger.Error("listener failed")
		return 1
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		logger.Warn("shutdown deadline exceeded; connections forced closed")
	} else {
		logger.Info("component stopped", "component", "ui")
	}
	if err := <-errch; err != nil && !errors.Is(err, uihttp.ErrServerClosed) {
		return 1
	}
	return 0
}
