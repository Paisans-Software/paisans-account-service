// Command paisans-invite serves member invites. See the README for its
// environment.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/config"
	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/idp/pocketid"
	"github.com/paisans-software/paisans-invite/internal/mail"
	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("INVITE_LISTEN_ADDR")))
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// healthcheck probes the local /healthz and returns the process exit code.
// The image has no shell, so the binary is its own health check.
func healthcheck(listenAddr string) int {
	if listenAddr == "" {
		listenAddr = ":8080"
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	c, err := coc.Load(cfg.CoCPath)
	if err != nil {
		return err
	}
	sender, err := mail.NewSMTP(cfg.SMTP)
	if err != nil {
		return err
	}
	provider := pocketid.New(cfg.IDPURL, cfg.IDPAPIKey, &http.Client{Timeout: 15 * time.Second})
	f := flow.New(st, provider, sender, c, flow.Settings{
		Quota: cfg.Quota, RequireApproval: cfg.RequireApproval, InviteLifetime: cfg.InviteLifetime,
		PublicURL: cfg.PublicURL, EmailHashKey: cfg.EmailHashKey,
	}, time.Now)
	handler, err := web.New(ctx, cfg, f, st)
	if err != nil {
		return err
	}

	go sweep(ctx, st, cfg)

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	slog.Info("listening", "addr", cfg.ListenAddr, "coc_version", c.Version, "require_approval", cfg.RequireApproval)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func sweep(ctx context.Context, st *store.Store, cfg config.Config) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		r, err := st.Sweep(ctx, time.Now(), cfg.ApplicationWait, cfg.DeclineRetention)
		if err != nil {
			slog.Warn("sweep failed", "error", err)
		} else if r.Ran {
			slog.Info("sweep", "invites_expired", r.InvitesExpired, "applications_expired", r.ApplicationsExpired, "declines_deleted", r.DeclinesDeleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
