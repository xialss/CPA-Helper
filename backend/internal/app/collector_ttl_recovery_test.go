package app

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCollectorTTLUnlimitedOutcomeSurvivesReceiptFailure(t *testing.T) {
	for _, markerFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("marker failure %t", markerFailure), func(t *testing.T) {
			a, owner, pricing := newCollectorTTLApp(t)
			_, cfg := newCollectorTTLFixture(t)
			ctx, now := context.Background(), time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
			if _, err := a.updateUserQuota(ctx, owner, userQuotaPayload{}); err != nil {
				t.Fatal(err)
			}
			id := collectorTTLID(90)
			raw := collectorTTLMessage(t, id, now, nil)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER fail_unlimited_outcome BEFORE UPDATE OF state ON usage_collector_inbox WHEN NEW.state = 'complete' BEGIN SELECT RAISE(ABORT, 'receipt failed'); END`
			if markerFailure {
				trigger = `CREATE TRIGGER fail_unlimited_outcome BEFORE UPDATE OF quota_handled ON usage_collector_inbox WHEN NEW.quota_handled = 1 BEGIN SELECT RAISE(ABORT, 'outcome failed'); END`
			}
			if _, err := a.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err == nil {
				t.Fatal("receipt/outcome persistence failure was hidden")
			}
			if got := collectorTTLCount(t, a, `SELECT quota_handled FROM usage_collector_inbox`); (got == 1) == markerFailure {
				t.Fatalf("successful no-charge outcome marker = %d", got)
			}
			if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`); got != 0 {
				t.Fatal("unlimited ingestion created an unnecessary ledger")
			}
			if _, err := a.db.Exec(`DROP TRIGGER fail_unlimited_outcome`); err != nil {
				t.Fatal(err)
			}
			// A failed outcome was never reported as handled; retry it while the
			// original unlimited policy still holds before activating quota.
			if markerFailure {
				if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			quota := 100.0
			if _, err := a.updateUserQuota(ctx, owner, userQuotaPayload{LifetimeQuotaUSD: &quota}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.processCollectorUsageInboxWithPricing(ctx, AppConfig{}, nil, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM user_quota_charges`); got != 0 {
				t.Fatal("receipt recovery charged usage completed before quota activation")
			}
			user, err := a.getUser(ctx, owner)
			if err != nil || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != quota {
				t.Fatalf("activated quota was charged retrospectively: %#v/%v", user, err)
			}
		})
	}
}

func TestCollectorTTLProtectedSourcesRetainIdentityAndAliases(t *testing.T) {
	a, _, pricing := newCollectorTTLApp(t)
	_, cfg := newCollectorTTLFixture(t)
	ctx, now := context.Background(), time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
	sources := []string{"sk-same-first-credential-1234", "sk-same-other-credential-1234"}
	if maskSecret(&sources[0]) != maskSecret(&sources[1]) {
		t.Fatal("fixture must have equal masks")
	}
	providers := []aiProviderItem{}
	want := []map[string]string{}
	var messages []string
	for index, source := range sources {
		hash := hashAPIKey(source)
		providers = append(providers, aiProviderItem{Brand: aiProviderBrandGemini, APIKeyHash: &hash, Models: []aiProviderModel{{Name: "one"}}})
		if _, err := a.db.Exec(`INSERT INTO model_price_channel_aliases (auth_type, channel_brand, channel_key, label, created_at, updated_at) VALUES ('apikey', 'gemini', ?, 'Same source', '', '')`, "keyhash:"+hash); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, collectorTTLMessage(t, collectorTTLID(100+index), now, func(raw map[string]any) {
			raw["source"], raw["provider"], raw["model"] = source, "gemini", "one"
			delete(raw, "auth_index")
			raw["tokens"].(map[string]any)["cache_creation_tokens"] = 0
			// Arbitrary client hash fields cannot replace the canonical digest.
			raw["source_key"] = strings.Repeat("0", 64)
		}))
		want = append(want, map[string]string{"key": *usageSourceKey(&source), "label": "Same source · " + maskSecret(&source)})
	}
	installUsageSourceOptionsSnapshot(t, a, providers)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, messages, now); err != nil {
		t.Fatal(err)
	}
	if count, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil || count != 2 {
		t.Fatalf("protected source ingestion = %d/%v", count, err)
	}
	labels, err := a.usageSourceLabels(ctx, a.attachCachedBillingPriceSelectors(modelPriceBillingIndex{}).MatchContext)
	if err != nil {
		t.Fatal(err)
	}
	for index, message := range messages {
		record := collectorTTLRecord(t, a, message)
		key := usageSourceKey(&sources[index])
		if !sameOptionalUsageText(record.analyticsSourceKey, key) || !usageRecordMatchesPostFilters(record, UsageFilters{SourceKey: key}) {
			t.Fatalf("record source identity changed after redaction: %#v", record)
		}
		if label := labels.labelFor(record); label == nil || *label != "Same source" {
			t.Fatalf("no-index alias lost authenticated source digest: %v", label)
		}
		for _, invalidRaw := range []string{`{}`, `{"auth_type":null}`, `{"auth_type":"oauth"}`, `{`} {
			corrected := record
			corrected.RawJSON = invalidRaw
			if label := labels.labelFor(corrected); label != nil {
				t.Fatalf("nonempty unauthenticated raw correction trusted retained digest: %s/%s", invalidRaw, *label)
			}
		}
		if strings.Contains(record.RawJSON, sources[index]) || strings.Contains(*record.Source, sources[index]) {
			t.Fatal("source identity repair restored plaintext key")
		}
		if _, err := a.db.Exec(`UPDATE usage_records SET raw_json = '' WHERE id = ?`, record.ID); err != nil {
			t.Fatal(err)
		}
		// Rebuild even after the fact itself was lost: the retained record digest
		// must survive independently of raw payload retention and the catalog.
		if _, err := a.db.Exec(`DELETE FROM usage_analytics_facts WHERE usage_record_id = ?`, record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Exec(`INSERT OR IGNORE INTO usage_analytics_pending_facts (usage_record_id) VALUES (?)`, record.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatal(err)
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(DISTINCT source_key) FROM usage_analytics_facts`); got != 2 {
		t.Fatalf("equal masks collapsed repaired source identities: %d", got)
	}
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, UsageFilters{Scope: "admin", Start: &start, End: &end})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(want, func(i, j int) bool { return want[i]["key"] < want[j]["key"] })
	if got := response["sources"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("protected source options = %#v, want %#v", got, want)
	}
	// Existing raw-only corrections must replace the retained digest as well
	// as the compact fact, rather than binding the correction to the old source.
	first := collectorTTLRecord(t, a, messages[0])
	correctedRaw := fmt.Sprintf(`{"source":%q,"auth_type":"api_key"}`, sources[1])
	if _, err := a.db.Exec(`UPDATE usage_records SET raw_json = ? WHERE id = ?`, correctedRaw, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatal(err)
	}
	corrected, err := a.getUsageRecord(ctx, first.ID)
	if err != nil || !sameOptionalUsageText(corrected.analyticsSourceKey, usageSourceKey(&sources[1])) {
		t.Fatalf("raw source correction retained stale record identity: %#v/%v", corrected.analyticsSourceKey, err)
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(DISTINCT source_key) FROM usage_analytics_facts`); got != 1 {
		t.Fatalf("raw source correction retained stale fact identity: %d", got)
	}
}

func TestCollectorTTLPendingRestartAndDeletedOwner(t *testing.T) {
	a, owner, pricing := newCollectorTTLApp(t)
	_, cfg := newCollectorTTLFixture(t)
	ctx, now := context.Background(), time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
	id := collectorTTLID(110)
	raw := collectorTTLMessage(t, id, now, nil)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE usage_collector_inbox SET state = 'pending', next_attempt_at = ?, deadline_at = ?`, dbTime(now.Add(time.Hour)), dbTime(now.Add(5*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`DELETE FROM user_api_keys WHERE user_id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`DELETE FROM users WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	replacement := seedQuotaTestUser(t, a, "collector-ttl-owner")
	seedQuotaTestAPIKey(t, a, replacement, collectorTTLTestKey)
	quota := 100.0
	if _, err := a.updateUserQuota(ctx, replacement, userQuotaPayload{LifetimeQuotaUSD: &quota}); err != nil {
		t.Fatal(err)
	}
	a.Close()
	restarted, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if count, err := restarted.processCollectorUsageInbox(ctx, cfg, pricing, now.Add(10*time.Second)); err != nil || count != 1 {
		t.Fatalf("pending restart = %d/%v", count, err)
	}
	record := collectorTTLRecord(t, restarted, raw)
	if record.CacheCreationTokens != 1000000 {
		t.Fatalf("restart lost the original cache creation total: %#v", record)
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges WHERE user_id = ?`, replacement); got != 0 {
		t.Fatal("deleted owner's usage charged a reused username/key")
	}
	user, err := restarted.getUser(ctx, replacement)
	if err != nil || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != quota {
		t.Fatalf("replacement user balance changed: %#v/%v", user, err)
	}
}

func TestCollectorTTLSourceCorrectionUpdatesScalarAndRaw(t *testing.T) {
	a, _, pricing := newCollectorTTLApp(t)
	_, cfg := newCollectorTTLFixture(t)
	ctx, now := context.Background(), time.Date(2026, 10, 28, 10, 0, 0, 0, appTimeLocation)
	before, after := "sk-before-source-credential-1234", "sk-after-source-credential-9876"
	afterHash := hashAPIKey(after)
	installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{{Brand: aiProviderBrandGemini, APIKeyHash: &afterHash, Models: []aiProviderModel{{Name: "one"}}}})
	if _, err := a.db.Exec(`INSERT INTO model_price_channel_aliases (auth_type, channel_brand, channel_key, label, created_at, updated_at) VALUES ('apikey', 'gemini', ?, 'Corrected source', '', '')`, "keyhash:"+afterHash); err != nil {
		t.Fatal(err)
	}
	raw := collectorTTLMessage(t, collectorTTLID(115), now, func(raw map[string]any) {
		raw["source"], raw["provider"], raw["model"] = before, "gemini", "one"
		delete(raw, "auth_index")
		raw["tokens"].(map[string]any)["cache_creation_tokens"] = 0
	})
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now, pricing); err != nil {
		t.Fatal(err)
	}
	if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil {
		t.Fatal(err)
	}
	record := collectorTTLRecord(t, a, raw)
	if _, err := a.db.Exec(`UPDATE usage_records SET source = ?, raw_json = ? WHERE id = ?`, after, fmt.Sprintf(`{"source":%q,"auth_type":"api_key"}`, after), record.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatal(err)
	}
	updated, err := a.getUsageRecord(ctx, record.ID)
	if err != nil || !sameOptionalUsageText(updated.analyticsSourceKey, usageSourceKey(&after)) {
		t.Fatalf("simultaneous source correction retained old digest: %#v/%v", updated.analyticsSourceKey, err)
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_analytics_facts WHERE source_key = ?`, *usageSourceKey(&after)); got != 1 {
		t.Fatalf("corrected fact was not assigned to B: %d", got)
	}
	for _, test := range []struct {
		source string
		want   int
	}{{before, 0}, {after, 1}} {
		key := usageSourceKey(&test.source)
		records, err := a.filteredUsageRecords(ctx, UsageFilters{SourceKey: key}, "timestamp ASC")
		if err != nil || len(records) != test.want {
			t.Fatalf("source filter %s = %d/%v, want %d", *key, len(records), err, test.want)
		}
	}
	response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, UsageFilters{Scope: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{{"key": *usageSourceKey(&after), "label": "Corrected source · " + maskSecret(&after)}}
	if got := response["sources"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("corrected source options = %#v, want %#v", got, want)
	}
	if _, err := a.db.Exec(`UPDATE usage_records SET raw_json = '' WHERE id = ?`, record.ID); err != nil {
		t.Fatal(err)
	}
	retained, err := a.getUsageRecord(ctx, record.ID)
	if err != nil || !sameOptionalUsageText(retained.analyticsSourceKey, usageSourceKey(&after)) {
		t.Fatalf("raw cleanup reverted corrected identity: %v", err)
	}
}
