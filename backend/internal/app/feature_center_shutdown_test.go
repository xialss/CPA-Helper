package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-helper/backend/internal/httpserver"
)

func TestFeatureCenterHTTPShutdownDrainsEventsBeforeOrdinaryRequests(t *testing.T) {
	app, routes, admin := newFeatureCenterTestApp(t)
	operationContext, finishOperation, err := app.beginAIRadarOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer finishOperation()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	requestStarted, releaseRequest := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/controlled-request" {
			routes.ServeHTTP(w, r)
			return
		}
		close(requestStarted)
		<-releaseRequest
		var users int
		if err := app.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users`).Scan(&users); err != nil || users != 1 {
			http.Error(w, fmt.Sprintf("ordinary request lost its database: users=%d, %v", users, err), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "drained")
	})
	ctx, cancel := context.WithCancel(context.Background())
	runDone, notified := make(chan error, 1), make(chan struct{})
	go func() {
		runDone <- httpserver.Run(ctx, httpserver.Config{
			Addr: addr, Handler: handler, ShutdownTimeout: 3 * time.Second,
			OnShutdown: func() {
				app.StopFeatureCenterEvents()
				app.StopFeatureCenterEvents() // shutdown notification is idempotent
				close(notified)
			},
		})
	}()
	var stream *http.Response
	returned := false
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseRequest) })
		cancel()
		if stream != nil {
			stream.Body.Close()
		}
		if !returned {
			select {
			case <-runDone:
			case <-time.After(5 * time.Second):
				t.Error("HTTP server did not finish after test cleanup")
			}
		}
	})
	baseURL := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(baseURL + "/api/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("HTTP server did not start: %v", err)
		case <-ticker.C:
		}
	}
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/features/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range admin {
		request.AddCookie(cookie)
	}
	stream, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", stream.StatusCode)
	}
	reader := bufio.NewReader(stream.Body)
	initial := featureCenterReadEvent(t, reader)
	streamDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		streamDone <- err
	}()
	ordinaryDone := make(chan error, 1)
	go func() {
		response, err := client.Get(baseURL + "/controlled-request")
		if err == nil {
			var body []byte
			body, err = io.ReadAll(response.Body)
			response.Body.Close()
			if err == nil && (response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "drained") {
				err = fmt.Errorf("ordinary request = %d %q", response.StatusCode, body)
			}
		}
		ordinaryDone <- err
	}()
	featureCenterWait(t, requestStarted, "ordinary HTTP request")
	cancel()
	featureCenterWait(t, notified, "HTTP shutdown event notification")
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("SSE did not end with EOF: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE did not stop while an ordinary request was draining")
	}
	select {
	case err := <-runDone:
		returned = true
		t.Fatalf("HTTP server returned before its ordinary request drained: %v", err)
	default:
	}
	if err := app.db.Ping(); err != nil || operationContext.Err() != nil {
		t.Fatalf("event notification stopped the database or radar: db=%v, radar=%v", err, operationContext.Err())
	}
	if current := featureCenterTestSettings(t, app); current != initial {
		t.Fatalf("event notification changed saved feature state: %#v", current)
	}
	if _, _, err := app.subscribeFeatureCenter(); err == nil {
		t.Fatal("new SSE subscription accepted after shutdown notification")
	} else {
		var appErr *AppError
		if !errors.As(err, &appErr) || appErr.Status != http.StatusServiceUnavailable {
			t.Fatalf("new SSE subscription error = %v", err)
		}
	}
	app.features.mu.Lock()
	subscribers := len(app.features.subscribers)
	app.features.mu.Unlock()
	if subscribers != 0 {
		t.Fatalf("HTTP shutdown retained %d SSE subscribers", subscribers)
	}
	releaseOnce.Do(func() { close(releaseRequest) })
	select {
	case err := <-ordinaryDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary request did not complete")
	}
	select {
	case err := <-runDone:
		returned = true
		if err != nil {
			t.Fatalf("HTTP shutdown = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP server did not finish draining")
	}
}
