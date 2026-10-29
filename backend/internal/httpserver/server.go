package httpserver

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Config struct {
	Addr            string
	Handler         http.Handler
	ShutdownTimeout time.Duration
	OnShutdown      func()
}

func Run(ctx context.Context, cfg Config) error {
	addr := cfg.Addr
	if addr == "" {
		addr = ":18317"
	}
	shutdownTimeout := cfg.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 10 * time.Second
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           cfg.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.OnShutdown != nil {
		server.RegisterOnShutdown(cfg.OnShutdown)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("CPA-Helper Go backend listening on %s", addr)
		errCh <- server.ListenAndServe()
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stopCh)

	select {
	case sig := <-stopCh:
		log.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Printf("server context canceled, shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}
