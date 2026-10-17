package security

import (
	"strings"
	"testing"
)

func TestMaskSecretKeepsShortSecretsHidden(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"a", "****"},
		{"abcd", "****"},
		{"abcde", "a...e"},
		{"abcdefgh", "a...h"},
		{"abcdefghi", "a...i"},
		{"abcdefghij", "a...j"},
		{"abcdefghijk", "abcdef...hijk"},
		{"abcdefghijklmnop", "abcdef...mnop"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := MaskSecret(&tc.raw)
			if got != tc.want {
				t.Fatalf("MaskSecret() = %q, want %q", got, tc.want)
			}
			if strings.ReplaceAll(got, ".", "") == tc.raw {
				t.Fatal("mask exposes the complete secret")
			}
		})
	}
}
