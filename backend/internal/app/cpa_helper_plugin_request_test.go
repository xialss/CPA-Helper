package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCPAHelperPluginRequestsDeriveManagementHTTPURL(t *testing.T) {
	const path = cpaHelperPluginManagementPrefix + "/policy"
	const payload = `{"policy_revision":2}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPut || r.URL.Path != path {
			t.Errorf("request = %s %s; want PUT %s", r.Method, r.URL.Path, path)
		}
		if r.Header.Get("Authorization") != "Bearer management-key" || r.Header.Get("If-Match") != `"1"` || r.Header.Get("Idempotency-Key") != "request-id" {
			t.Errorf("management request lost authentication or preconditions: %#v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != payload {
			t.Errorf("request body = %s, err=%v; want %s", body, err, payload)
		}
		w.Header().Set("ETag", `"2"`)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	app := &App{}
	for _, scheme := range []string{"http", "resp", "tcp", "redis"} {
		t.Run(scheme, func(t *testing.T) {
			cfg := AppConfig{Collector: CollectorConfig{
				CLIProxyURL: scheme + "://" + strings.TrimPrefix(server.URL, "http://"), ManagementKey: "management-key",
			}}
			headers := http.Header{"If-Match": {`"1"`}, "Idempotency-Key": {"request-id"}}
			status, responseHeaders, body, err := app.cpaHelperPluginRequest(context.Background(), cfg, http.MethodPut, path, []byte(payload), headers)
			if err != nil || status != http.StatusOK || responseHeaders.Get("ETag") != `"2"` || string(body) != payload {
				t.Fatalf("management response = %d %s, err=%v; want 200 %s", status, body, err, payload)
			}
		})
	}
	if requests.Load() != 4 {
		t.Fatalf("upstream requests = %d, want 4", requests.Load())
	}

	cfg := AppConfig{Collector: CollectorConfig{CLIProxyURL: "ftp://127.0.0.1:8317", ManagementKey: "management-key"}}
	_, _, _, err := app.cpaHelperPluginRequest(context.Background(), cfg, http.MethodGet, path, nil, nil)
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != "validation_error" || requests.Load() != 4 {
		t.Fatalf("invalid management URL = %v, requests=%d; want validation_error without request", err, requests.Load())
	}
}
