package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestV8ConfigReturnsRootConfiguration(t *testing.T) {
	fixture := newFakeCPA()
	request := httptest.NewRequest(http.MethodGet, "/v8/management/config", nil)
	request.Header.Set("Authorization", "Bearer acceptance-management-key")
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("config status = %d", response.Code)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"api-keys", "access", "plugins"} {
		var section map[string]json.RawMessage
		if err := json.Unmarshal(config[field], &section); err != nil || section == nil {
			t.Fatalf("native v8 config is missing the root %q object: %v", field, err)
		}
	}
}

func TestValidateLoopbackListenAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		wantErr bool
	}{
		{address: "127.0.0.1:18319"},
		{address: "localhost:18319"},
		{address: "[::1]:18319"},
		{address: ":18319", wantErr: true},
		{address: "0.0.0.0:18319", wantErr: true},
		{address: "192.168.1.10:18319", wantErr: true},
		{address: "127.0.0.1", wantErr: true},
	} {
		t.Run(test.address, func(t *testing.T) {
			err := validateLoopbackListenAddress(test.address)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateLoopbackListenAddress(%q) error = %v, wantErr %t", test.address, err, test.wantErr)
			}
		})
	}
}
