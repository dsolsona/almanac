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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/backendhttp"
	"github.com/dsolsona/almanac/internal/config"
	"github.com/dsolsona/almanac/internal/storage"
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
	case "setup":
		return setup(ctx, args[1:], stdout, stderr)
	case "auth":
		return authCommand(ctx, args[1:], stdout, stderr)
	case "agents":
		return agentsCommand(ctx, args[1:], stdout, stderr)
	case "datasources", "providers", "slis":
		return datasourceCommand(ctx, args, stdout, stderr)
	case "secrets":
		return secretsCommand(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "almanac: unsupported command %q\n", args[0])
		return 2
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  almanac version [--output=json]
  almanac serve [--config=FILE] [--components=LIST] [--listen=ADDR] [--backend-url=ORIGIN] [--database-url-file=FILE] [--initial-admin-login=LOGIN] [--initial-admin-password-file=FILE] [--public-origin=ORIGIN] [--tls-cert-file=FILE] [--tls-key-file=FILE] [--trusted-proxy-cidrs=LIST] [--shutdown-timeout=DURATION]
  almanac db upgrade [--database-url-file=FILE] [--timeout=30s] [--output=json]
  almanac setup --backend-url=ORIGIN --initial-admin-login=LOGIN --initial-admin-password-file=FILE --new-password-file=FILE [--ca-file=PEM] [--timeout=30s] [--output=json]
  almanac auth login --profile-file=FILE --backend-url=ORIGIN --login=LOGIN --password-file=FILE [--ca-file=PEM] [--timeout=30s] [--output=json]
  almanac auth context --profile-file=FILE [--timeout=30s] [--output=json]
  almanac auth logout --profile-file=FILE [--timeout=30s] [--output=json]
  almanac agents list --profile-file=FILE [--limit=50] [--cursor=CURSOR] [--timeout=30s] [--output=json]
  almanac agents get --profile-file=FILE --agent-id=UUID
  almanac agents credentials list --profile-file=FILE --agent-id=UUID [--limit=50] [--cursor=CURSOR]
  almanac agents create --profile-file=FILE --name=NAME --display-name=NAME --idempotency-key=KEY (--token-file=FILE|--show-token)
  almanac agents credentials rotate --profile-file=FILE --agent-id=UUID --if-match=ETAG --idempotency-key=KEY --confirm (--token-file=FILE|--show-token)
  almanac agents credentials revoke --profile-file=FILE --agent-id=UUID --credential-id=UUID --if-match=ETAG --idempotency-key=KEY --confirm
  almanac agents disable --profile-file=FILE --agent-id=UUID --if-match=ETAG --idempotency-key=KEY --confirm
  almanac agents datasources --profile-file=FILE --agent-id=UUID
  almanac agents delete --profile-file=FILE --agent-id=UUID --if-match=ETAG --idempotency-key=KEY --confirm
  almanac datasources list --profile-file=FILE [--limit=50] [--cursor=CURSOR] [--output=json]
  almanac datasources get --profile-file=FILE --datasource-id=UUID [--output=json]
  almanac datasources create --profile-file=FILE --input-file=FILE --idempotency-key=KEY [--output=json]
  almanac datasources update --profile-file=FILE --datasource-id=UUID --if-match=ETAG --input-file=FILE [--output=json]
  almanac datasources test --profile-file=FILE --input-file=FILE [--wait] [--cancel-on-timeout]
  almanac providers list --profile-file=FILE [--agent-id=UUID] [--output=json]
  almanac slis list --profile-file=FILE [--limit=50] [--cursor=CURSOR]
  almanac slis get --profile-file=FILE --sli-id=UUID
  almanac slis create --profile-file=FILE --input-file=FILE --idempotency-key=KEY
  almanac slis update --profile-file=FILE --sli-id=UUID --if-match=ETAG --input-file=FILE
  almanac slis observations --profile-file=FILE --sli-id=UUID [--limit=50] [--cursor=CURSOR]
  almanac slis test --profile-file=FILE --datasource-id=UUID --query=PROMQL [--wait] [--cancel-on-timeout]
  almanac secrets provision-source-keyset --file=FILE --keyset-id=UUID [--output=json]

Implemented component modes:
  backend  Serve protected local authentication API with PostgreSQL ownership.
  ui       Serve the embedded web UI and proxy /api/v1/ to --backend-url.
  backend,ui or ui,backend  Serve the embedded UI and authentication API on one listener.

Unsupported modes:
  alerts and other combined modes are not implemented yet and fail before listening.`)
}

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "strict JSON configuration file")
	fs.String("listen", "", "HTTP listen address")
	fs.String("components", "", "comma-separated components")
	fs.String("backend-url", "", "fixed backend origin for UI mode")
	fs.String("shutdown-timeout", "", "graceful shutdown deadline")
	fs.String("database-url-file", "", "protected file containing a PostgreSQL URL")
	fs.String("initial-admin-login", "", "initial administrator login")
	fs.String("initial-admin-password-file", "", "protected initial administrator password file")
	fs.String("public-origin", "", "canonical HTTPS public origin")
	fs.String("tls-cert-file", "", "public TLS certificate chain")
	fs.String("tls-key-file", "", "protected TLS private key")
	fs.String("trusted-proxy-cidrs", "", "trusted TLS-terminating proxy CIDRs")
	fs.String("source-secret-keyset-file", "", "protected source-secret Tink keyset file")
	fs.String("source-secret-keyset-id", "", "canonical UUID bound to the source-secret keyset")
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
		if cfg.Components != "backend" && !cfg.BackendUI() {
			fmt.Fprintf(stderr, "serve: components %q are not implemented; no listener was opened\n", cfg.Components)
			return 2
		}
		return serveBackend(ctx, cfg, stderr)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	server, err := uihttp.New(cfg, logger)
	if err != nil {
		fmt.Fprintln(stderr, "serve: configuration is invalid")
		return 2
	}
	return runServer(ctx, server, cfg, logger, stderr)
}

func serveBackend(parent context.Context, cfg config.Config, stderr io.Writer) (exitCode int) {
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	var assets http.Handler
	if cfg.BackendUI() {
		var err error
		assets, err = uihttp.NewAssetHandler()
		if err != nil {
			logger.Error("embedded UI assets unavailable")
			return 2
		}
	}
	if !authhttp.ValidPublicOrigin(cfg.PublicOrigin) {
		fmt.Fprintln(stderr, "serve: public_origin must be a canonical HTTPS origin")
		return 2
	}
	databaseURL, err := readDatabaseURLFile(cfg.DatabaseURLFile)
	if err != nil {
		fmt.Fprintln(stderr, "serve: database URL configuration is invalid")
		return 2
	}
	tlsConfig, err := backendhttp.LoadTLSConfig(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		fmt.Fprintln(stderr, "serve: TLS configuration is invalid")
		return 2
	}
	startupCtx, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	owner, err := storage.AcquireBackendOwner(startupCtx, databaseURL)
	if err != nil {
		logger.Error("backend ownership acquisition failed")
		return 1
	}
	defer func() {
		if err := owner.Close(); err != nil {
			logger.Error("backend ownership cleanup failed")
			exitCode = 1
		}
	}()
	initCtx, cancelInit := context.WithCancelCause(startupCtx)
	initMonitorDone := make(chan struct{})
	go func() {
		defer close(initMonitorDone)
		select {
		case <-owner.Done():
			cancelInit(storage.ErrBackendOwnershipLost)
		case <-initCtx.Done():
		}
	}()
	defer func() { cancelInit(context.Canceled); <-initMonitorDone }()
	if err := initCtx.Err(); err != nil {
		return 1
	}
	service, err := authentication.New(initCtx, databaseURL, time.Now)
	if err != nil {
		logger.Error("backend authentication initialization failed")
		return 1
	}
	lifeCtx, cancelLife := context.WithCancelCause(context.Background())
	defer cancelLife(context.Canceled)
	server, err := backendhttp.New(lifeCtx, cfg, service, databaseURL, tlsConfig, assets)
	if err != nil {
		logger.Error("backend server initialization failed")
		return 2
	}
	defer server.Close()
	server.RegisterOnShutdown(func() {
		logger.Info("component draining", "component", "backend")
	})
	lossWatcherDone := make(chan struct{})
	go func() {
		defer close(lossWatcherDone)
		select {
		case <-owner.Done():
			cancelLife(storage.ErrBackendOwnershipLost)
		case <-lifeCtx.Done():
		}
	}()
	defer func() { cancelLife(context.Canceled); <-lossWatcherDone }()
	if err := initCtx.Err(); err != nil {
		return 1
	}
	provision, provisionErr := service.EnsureInitialOwner(initCtx, cfg.InitialAdminLogin, cfg.InitialAdminPasswordFile, "initial-owner-bootstrap")
	if provisionErr != nil && !errors.Is(provisionErr, authentication.ErrInitialCredentials) {
		logger.Error("backend initial owner storage failed")
		return 1
	}
	if provisionErr == nil && provision.State.Provisioned {
		server.SetProvisioned(true)
	}
	if errors.Is(provisionErr, authentication.ErrInitialCredentials) {
		logger.Warn("backend initial owner is not provisioned; correct protected credentials and restart")
	}
	if owner.Err() != nil || startupCtx.Err() != nil {
		return 1
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("backend listener failed", "address", cfg.Listen)
		return 1
	}
	select {
	case <-owner.Done():
		_ = listener.Close()
		logger.Error("backend ownership lost")
		return 1
	case <-startupCtx.Done():
		_ = listener.Close()
		return 1
	default:
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("component started", "component", "backend", "address", listener.Addr().String())
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		logger.Error("backend server failed")
		return 1
	case <-owner.Done():
		cancelLife(storage.ErrBackendOwnershipLost)
		_ = server.Close()
		<-serveErr
		logger.Error("backend ownership lost")
		return 1
	case <-startupCtx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	shutdownErr := make(chan error, 1)
	go func() { shutdownErr <- server.Shutdown(shutdownCtx) }()
	var shutdownResult error
	select {
	case <-owner.Done():
		cancelLife(storage.ErrBackendOwnershipLost)
		_ = server.Close()
		shutdownResult = <-shutdownErr
	case shutdownResult = <-shutdownErr:
	}
	if shutdownResult != nil {
		_ = server.Close()
	}
	cancel()
	serveResult := <-serveErr
	if owner.Err() != nil {
		logger.Error("backend ownership lost during shutdown")
		return 1
	}
	if shutdownResult != nil || (serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed)) {
		logger.Error("backend shutdown failed", "deadline_exceeded", errors.Is(shutdownResult, context.DeadlineExceeded), "cancelled", errors.Is(shutdownResult, context.Canceled), "serve_failed", serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed))
		return 1
	}
	logger.Info("component stopped", "component", "backend")
	return 0
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
