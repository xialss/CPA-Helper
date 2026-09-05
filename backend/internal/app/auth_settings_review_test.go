package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseStringListRejectsMalformedRemoteAPIKeyLists(t *testing.T) {
	for _, payload := range []string{
		`{"api-keys": {}}`,
		`[123]`,
		`{"items": ["sk-valid", 123]}`,
		`{"error": "upstream failed"}`,
	} {
		t.Run(payload, func(t *testing.T) {
			if keys, err := parseStringList([]byte(payload)); err == nil {
				t.Fatalf("parseStringList(%s) = %#v, want an error", payload, keys)
			}
		})
	}
}

func TestParseStringListAcceptsSupportedShapes(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    int
	}{
		{name: "array", payload: `["sk-one", "sk-two"]`, want: 2},
		{name: "wrapped", payload: `{"api_keys": ["sk-one"]}`, want: 1},
		{name: "null", payload: `null`, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			keys, err := parseStringList([]byte(test.payload))
			if err != nil || len(keys) != test.want {
				t.Fatalf("parseStringList(%s) = %#v, %v; want %d keys", test.payload, keys, err, test.want)
			}
		})
	}
}

func TestSessionCookieSecureTransportPolicy(t *testing.T) {
	httpRequest := httptest.NewRequest(http.MethodGet, "http://example.test/api/auth/login", nil)
	if sessionCookieSecure(httpRequest) {
		t.Fatal("HTTP request unexpectedly marked the session cookie Secure")
	}

	httpsRequest := httptest.NewRequest(http.MethodGet, "https://example.test/api/auth/login", nil)
	if !sessionCookieSecure(httpsRequest) {
		t.Fatal("HTTPS request did not mark the session cookie Secure")
	}

	proxiedRequest := httptest.NewRequest(http.MethodGet, "http://example.test/api/auth/login", nil)
	proxiedRequest.Header.Set("X-Forwarded-Proto", "https")
	if !sessionCookieSecure(proxiedRequest) {
		t.Fatal("HTTPS reverse-proxy request did not mark the session cookie Secure")
	}
}
