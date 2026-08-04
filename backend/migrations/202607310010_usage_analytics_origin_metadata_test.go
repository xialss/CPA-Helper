package migrations

import "testing"

func TestUsageAnalyticsOriginMetadataPayloadFromRawJSONMatchesNormalizerAliases(t *testing.T) {
	payload := usageAnalyticsOriginMetadataPayloadFromRawJSON(`{
		"event": {
			"Origin": "nested-origin@example.com",
			"AccountEmail": "Origin-Owner@Example.com",
			"Auth_Type": "oauth",
			"AuthIndex": "nested-auth-index"
		}
	}`)
	if payload.source != "nested-origin@example.com" {
		t.Fatalf("source = %q, want nested origin", payload.source)
	}
	if payload.sourceAccount != "origin-owner@example.com" {
		t.Fatalf("source account = %q, want normalized nested account", payload.sourceAccount)
	}
	if payload.auth != "oauth" {
		t.Fatalf("auth = %q, want oauth", payload.auth)
	}
	if payload.authIndex != "nested-auth-index" {
		t.Fatalf("auth index = %q, want nested index", payload.authIndex)
	}
}

func TestUsageAnalyticsOriginMetadataPayloadUsesNormalizerScalarFormatting(t *testing.T) {
	payload := usageAnalyticsOriginMetadataPayloadFromRawJSON(`{
		"event": {
			"Origin": 1e20,
			"Authentication": true,
			"AccountId": 7
		}
	}`)
	if payload.source != "100000000000000000000" {
		t.Fatalf("source = %q, want normalizer float formatting", payload.source)
	}
	if payload.auth != "true" {
		t.Fatalf("auth = %q, want normalizer bool formatting", payload.auth)
	}
	if payload.authIndex != "7" {
		t.Fatalf("auth index = %q, want normalizer float formatting", payload.authIndex)
	}
}
