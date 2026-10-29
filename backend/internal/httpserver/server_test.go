package httpserver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunShutdownNotificationPreservesDrainTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	started, release, notified := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var notifications atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, Config{
			Addr: addr, ShutdownTimeout: 50 * time.Millisecond,
			OnShutdown: func() {
				notifications.Add(1)
				close(notified)
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(http.StatusOK)
					return
				}
				close(started)
				<-release
				w.WriteHeader(http.StatusOK)
			}),
		})
	}()
	returned := false
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		if !returned {
			select {
			case <-runDone:
			case <-time.After(2 * time.Second):
				t.Error("HTTP server did not stop")
			}
		}
	})
	client := &http.Client{Timeout: 2 * time.Second}
	baseURL := "http://" + addr
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(baseURL + "/health")
		if err == nil {
			response.Body.Close()
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("HTTP server did not listen: %v", err)
		case <-ticker.C:
		}
	}
	requestDone := make(chan error, 1)
	go func() {
		response, err := client.Get(baseURL + "/blocked")
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("controlled request did not start")
	}
	cancel()
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP shutdown did not notify streaming handlers")
	}
	select {
	case err := <-runDone:
		returned = true
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown error = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP shutdown did not respect its timeout")
	}
	if notifications.Load() != 1 {
		t.Fatalf("shutdown notifications = %d, want one", notifications.Load())
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controlled request did not clean up")
	}
}
