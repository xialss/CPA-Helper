package cpahttp

import "testing"

func TestEnsurePublicHTTPSURLRejectsPrivateTargets(t *testing.T) {
	for _, sourceURL := range []string{
		"http://example.com/prices.json",
		"https://127.0.0.1/prices.json",
		"https://[::1]/prices.json",
		"https://169.254.169.254/latest/meta-data",
		"https://localhost/prices.json",
	} {
		t.Run(sourceURL, func(t *testing.T) {
			if err := EnsurePublicHTTPSURL(sourceURL); err == nil {
				t.Fatalf("EnsurePublicHTTPSURL(%q) succeeded, want rejection", sourceURL)
			}
		})
	}
}
