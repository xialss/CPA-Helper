package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	backendApp "cpa-helper/backend/internal/app"
)

func TestServeDrainsAuthenticatedFeatureEvents(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	if _, err := backendApp.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPA_HELPER_ADDR", addr)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serve(ctx, backendApp.NewOptions{RequireReady: true, StartBackground: false})
	}()
	var eventResponse *http.Response
	returned := false
	t.Cleanup(func() {
		cancel()
		if eventResponse != nil {
			eventResponse.Body.Close()
		}
		if !returned {
			select {
			case <-serveDone:
			case <-time.After(3 * time.Second):
				t.Error("serve did not finish after test clients disconnected")
			}
		}
	})
	baseURL := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second}
	waitForShutdownTestServer(t, client, baseURL, serveDone)
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/auth/setup", strings.NewReader(`{"username":"shutdown-admin","password":"test-password","nickname":"Admin"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	setupResponse, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(setupResponse.Body)
	setupResponse.Body.Close()
	if readErr != nil || setupResponse.StatusCode != http.StatusOK {
		t.Fatalf("setup = %d %q, %v", setupResponse.StatusCode, body, readErr)
	}
	request, err = http.NewRequest(http.MethodGet, baseURL+"/api/features/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", baseURL)
	for _, cookie := range setupResponse.Cookies() {
		request.AddCookie(cookie)
	}
	eventResponse, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if eventResponse.StatusCode != http.StatusOK || eventResponse.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("event response = %d %v", eventResponse.StatusCode, eventResponse.Header)
	}
	reader := bufio.NewReader(eventResponse.Body)
	dataSeen := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		dataSeen = dataSeen || strings.HasPrefix(line, "data: ")
		if strings.TrimSpace(line) == "" && dataSeen {
			break
		}
	}
	streamDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		streamDone <- err
	}()
	cancel()
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("event stream did not end with EOF during shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated feature event stream prevented HTTP shutdown")
	}
	select {
	case err := <-serveDone:
		returned = true
		if err != nil {
			t.Fatalf("serve shutdown = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not finish after the event stream ended")
	}
}

func waitForShutdownTestServer(t *testing.T, client *http.Client, baseURL string, serveDone <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastError error
	for {
		response, err := client.Get(baseURL + "/api/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
			err = fmt.Errorf("health returned %d", response.StatusCode)
		}
		lastError = err
		select {
		case err := <-serveDone:
			t.Fatalf("serve returned before listening: %v", err)
		case <-deadline.C:
			t.Fatalf("serve did not start: %v", lastError)
		case <-ticker.C:
		}
	}
}
