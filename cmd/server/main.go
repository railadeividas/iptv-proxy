package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"iptv-proxy/internal/config"
	"iptv-proxy/internal/epg"
	"iptv-proxy/internal/events"
	"iptv-proxy/internal/health"
	"iptv-proxy/internal/proxy"
	"iptv-proxy/internal/server"
	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
	isync "iptv-proxy/internal/sync"
	"iptv-proxy/internal/version"
	"iptv-proxy/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		if err := resetPasswordCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	cfg := config.Load()
	if err := run(cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// setLogFormat switches the log output between JSON and plain text.
func setLogFormat(format string) {
	var h slog.Handler = slog.NewTextHandler(os.Stdout, nil)
	if format == "json" {
		h = slog.NewJSONHandler(os.Stdout, nil)
	}
	slog.SetDefault(slog.New(h))
}

func run(cfg config.Config) error {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	set := settings.New(st)
	setLogFormat(set.String("log_format"))
	set.OnChange(func(changed []string) {
		for _, k := range changed {
			if k == "log_format" {
				setLogFormat(set.String("log_format"))
			}
		}
	})

	signer, err := proxy.LoadKey(filepath.Join(cfg.DataDir, "secret.key"))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := events.NewHub()
	st.Notify = hub.Notify
	syncer := isync.New(st, set)
	px := proxy.New(signer, set)
	ep := epg.New(st, set, cfg.EPGDir, cfg.DataDir)
	syncer.OnProfilesChanged = ep.MarkDirty
	hc := health.New(st, set)

	go syncer.Run(ctx)
	go ep.Run(ctx)
	go hc.Run(ctx)

	app := server.New(cfg, st, set, syncer, px, ep, hc, signer, hub, web.FS())
	go app.Start(ctx)
	statsDone := make(chan struct{})
	go func() { app.Activity().Run(ctx); close(statsDone) }() // saves the running sessions when it ends

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// no Read/WriteTimeout: live streams stay open indefinitely
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	slog.Info("listening", "version", version.String(), "addr", cfg.Addr, "data", cfg.DataDir)
	err = srv.ListenAndServe()
	stop()
	select { // let the last sessions reach the database before it closes
	case <-statsDone:
	case <-time.After(5 * time.Second):
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
