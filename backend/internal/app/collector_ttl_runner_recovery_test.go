package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCollectorTTLRunnerLocalRecoveryWithoutPricingSelectors(t *testing.T) {
	for _, changedOrigin := range []bool{false, true} {
		name := "same origin"
		if changedOrigin {
			name = "changed origin"
		}
		t.Run(name, func(t *testing.T) {
			a, owner, pricing := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			ctx, now := context.Background(), time.Now().In(appTimeLocation)
			chargedID, pendingID, unhandledID := collectorTTLID(120), collectorTTLID(121), collectorTTLID(122)
			chargedRaw := collectorTTLMessage(t, chargedID, now, nil)
			f.facts[chargedID] = collectorTTLFact(chargedID, "1h", now)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{chargedRaw}, now, pricing); err != nil {
				t.Fatal(err)
			}
			if _, err := a.db.Exec(`CREATE TRIGGER fail_local_receipt BEFORE UPDATE OF state ON usage_collector_inbox WHEN NEW.state = 'complete' BEGIN SELECT RAISE(ABORT, 'receipt crash'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := a.processCollectorUsageInbox(ctx, cfg, pricing, now); err == nil {
				t.Fatal("fixture did not reach charged/pre-complete crash window")
			}
			if _, err := a.db.Exec(`DROP TRIGGER fail_local_receipt`); err != nil {
				t.Fatal(err)
			}
			charged := collectorTTLRecord(t, a, chargedRaw)
			assertCollectorTTLCharge(t, a, charged.ID, 8, false)
			old := now.Add(-6 * time.Minute)
			// Old envelopes may have no classification field at all.
			pendingRaw := collectorTTLMessage(t, pendingID, old, nil)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{pendingRaw}, old); err != nil {
				t.Fatal(err)
			}
			unhandledRaw := collectorTTLMessage(t, unhandledID, now, nil)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{unhandledRaw}, now, pricing); err != nil {
				t.Fatal(err)
			}
			items, err := a.collectorUsageInboxDue(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.ExecutionID != nil && *item.ExecutionID == unhandledID {
					resolveCollectorUsageMetadata(&item, collectorTTLFact(unhandledID, "1h", now))
					if err := a.persistCollectorUsageResolution(ctx, item, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			runtimeConfig, err := a.loadConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			runtimeConfig.Collector = cfg.Collector
			runtimeConfig.Collector.Enabled, runtimeConfig.Collector.PollIntervalSeconds, runtimeConfig.Collector.RetryIntervalSeconds = true, .2, 1
			if changedOrigin {
				runtimeConfig.Collector.CLIProxyURL += "/new-instance"
			}
			if err := a.saveConfig(ctx, runtimeConfig); err != nil {
				t.Fatal(err)
			}
			a.Close()
			restarted, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			runner := NewCollectorRunner(restarted)
			// The fixture deliberately serves no provider-selector routes. Its
			// metadata API is separate, while channel pricing requires selectors.
			if _, err := runner.loadCollectorPricing(ctx, runtimeConfig.Collector); err == nil {
				t.Fatal("offline selector fixture did not fail closed")
			}
			runner.Start()
			defer runner.Stop()
			deadline := time.Now().Add(4 * time.Second)
			for {
				state, err := restarted.collectorState(ctx)
				if err != nil {
					t.Fatal(err)
				}
				complete := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id = ? AND state = 'complete'`, chargedID)
				frozen := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id = ? AND state = 'ready'`, pendingID)
				if complete == 1 && frozen == 1 && state.LastError != nil && strings.Contains(*state.LastError, "404") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("local recovery stayed behind selector failure: complete=%d frozen=%d state=%#v", complete, frozen, state)
				}
				time.Sleep(10 * time.Millisecond)
			}
			runner.Stop()
			if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_records`); got != 1 {
				t.Fatalf("unhandled usage entered accounting without reliable pricing: %d", got)
			}
			if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges`); got != 1 {
				t.Fatalf("local recovery replayed/created a quota charge: %d", got)
			}
			if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id = ? AND (payload_json != '' OR caller_scope IS NOT NULL OR usage_user_id IS NOT NULL)`, chargedID); got != 0 {
				t.Fatal("handled recovery retained processing payload")
			}
			remaining, err := restarted.collectorUsageInboxDue(ctx, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range remaining {
				if item.ExecutionID != nil && *item.ExecutionID == pendingID {
					metadata := item.Envelope.Usage.CacheCreationMetadata
					if item.State != "ready" || metadata.Status == nil || *metadata.Status != "missing" || metadata.Source == nil || *metadata.Source != "unknown" {
						t.Fatalf("deadline invented TTL evidence: %#v", metadata)
					}
				}
			}
			user, err := restarted.getUser(ctx, owner)
			if err != nil || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != 992 {
				t.Fatalf("pricing outage changed quota: %#v/%v", user, err)
			}
			f.mu.Lock()
			calls, queues := f.calls, f.queueCalls
			f.mu.Unlock()
			if calls != 1 || queues != 0 {
				t.Fatalf("local recovery re-queried completed metadata or consumed queue: %d/%d", calls, queues)
			}
		})
	}
}

func TestCollectorTTLRunnerReadsMetadataWhilePricingUnavailable(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "frozen applicability"
		if legacy {
			name = "older pending envelope"
		}
		t.Run(name, func(t *testing.T) {
			a, _, pricing := newCollectorTTLApp(t)
			f, cfg := newCollectorTTLFixture(t)
			ctx, now := context.Background(), time.Now().In(appTimeLocation)
			id := collectorTTLID(123)
			raw := collectorTTLMessage(t, id, now, nil)
			if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{raw}, now, pricing); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if _, err := a.db.Exec(`UPDATE usage_collector_inbox SET payload_json = json_remove(payload_json, '$.claude_applicable'), last_error_code = 'metadata_missing', attempts = 1`); err != nil {
					t.Fatal(err)
				}
			}
			f.facts[id] = collectorTTLFact(id, "1h", now)
			runtimeConfig, err := a.loadConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			runtimeConfig.Collector = cfg.Collector
			runtimeConfig.Collector.Enabled, runtimeConfig.Collector.PollIntervalSeconds, runtimeConfig.Collector.RetryIntervalSeconds = true, .2, 1
			if err := a.saveConfig(ctx, runtimeConfig); err != nil {
				t.Fatal(err)
			}
			a.Close()
			restarted, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			runner := NewCollectorRunner(restarted)
			runner.Start()
			defer runner.Stop()
			deadline := time.Now().Add(4 * time.Second)
			for {
				state, err := restarted.collectorState(ctx)
				if err != nil {
					t.Fatal(err)
				}
				ready := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE state = 'ready'`)
				if ready == 1 && state.LastError != nil && strings.Contains(*state.LastError, "404") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("metadata stayed blocked on independent pricing selectors: %#v", state)
				}
				time.Sleep(10 * time.Millisecond)
			}
			runner.Stop()
			items, err := restarted.collectorUsageInboxDue(ctx, time.Now())
			if err != nil || len(items) != 1 {
				t.Fatalf("resolved pending input = %d/%v", len(items), err)
			}
			usage := items[0].Envelope.Usage
			if usage.CacheCreationTTL == nil || *usage.CacheCreationTTL != "1h" || usage.CacheCreationMetadata.Source == nil || *usage.CacheCreationMetadata.Source != "enforced_request" {
				t.Fatalf("available metadata was lost during pricing outage: %#v", usage.CacheCreationMetadata)
			}
			if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_records`); got != 0 {
				t.Fatal("metadata-only recovery created an accounting record without pricing")
			}
			if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges`); got != 0 {
				t.Fatal("metadata-only recovery charged against unavailable selectors")
			}
			f.mu.Lock()
			calls, queues := f.calls, f.queueCalls
			f.mu.Unlock()
			if calls != 1 || queues != 0 {
				t.Fatalf("outage recovery did unexpected IO: metadata=%d queue=%d", calls, queues)
			}
		})
	}
}

func TestCollectorTTLRunnerRecoveryBeyondUnpricedReadyBatch(t *testing.T) {
	a, owner, pricing := newCollectorTTLApp(t)
	f, cfg := newCollectorTTLFixture(t)
	ctx, now := context.Background(), time.Now().In(appTimeLocation)
	quota := 20000.0
	if _, err := a.updateUserQuota(ctx, owner, userQuotaPayload{LifetimeQuotaUSD: &quota}); err != nil {
		t.Fatal(err)
	}
	// Fill an entire low-ID batch with resolved inputs that still require their
	// first reliable price calculation. Metadata-only scans must pass them.
	prefix := make([]string, collectorTTLBatchSize)
	for index := range prefix {
		id := collectorTTLID(10000 + index)
		prefix[index] = collectorTTLMessage(t, id, now, nil)
		f.facts[id] = collectorTTLFact(id, "1h", now)
	}
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, prefix, now, pricing); err != nil {
		t.Fatal(err)
	}
	if completed, err := a.processCollectorUsageInboxWithPricing(ctx, cfg, nil, now); err != nil || completed != 0 {
		t.Fatalf("prepare full unpriced ready batch = %d/%v", completed, err)
	}
	chargedID, expiredID, matchedID := collectorTTLID(20000), collectorTTLID(20001), collectorTTLID(20002)
	chargedRaw := collectorTTLMessage(t, chargedID, now, nil)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{chargedRaw}, now, pricing); err != nil {
		t.Fatal(err)
	}
	items, err := a.collectorUsageInboxDueForPricing(ctx, now, false)
	if err != nil || len(items) != 1 || items[0].ExecutionID == nil || *items[0].ExecutionID != chargedID {
		t.Fatalf("metadata work set stayed behind the full ready batch: %d/%v", len(items), err)
	}
	chargedItem := items[0]
	resolveCollectorUsageMetadata(&chargedItem, collectorTTLFact(chargedID, "1h", now))
	if err := a.persistCollectorUsageResolution(ctx, chargedItem, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE TRIGGER fail_batch_receipt BEFORE UPDATE OF state ON usage_collector_inbox WHEN NEW.state = 'complete' BEGIN SELECT RAISE(ABORT, 'receipt crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.finalizeCollectorUsage(ctx, chargedItem, pricing, now); err == nil {
		t.Fatal("fixture did not reach charged/pre-complete crash window")
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_batch_receipt`); err != nil {
		t.Fatal(err)
	}
	if got := collectorTTLCount(t, a, `SELECT COUNT(*) FROM usage_collector_inbox WHERE id < ? AND state = 'ready' AND quota_handled = 0`, chargedItem.ID); got != collectorTTLBatchSize {
		t.Fatalf("low-ID unhandled ready prefix = %d", got)
	}
	old := now.Add(-6 * time.Minute)
	expiredRaw := collectorTTLMessage(t, expiredID, old, nil)
	matchedRaw := collectorTTLMessage(t, matchedID, now, nil)
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{expiredRaw}, old, pricing); err != nil {
		t.Fatal(err)
	}
	if err := a.persistCollectorUsageBatch(ctx, cfg.Collector, []string{matchedRaw}, now, pricing); err != nil {
		t.Fatal(err)
	}
	f.facts[matchedID] = collectorTTLFact(matchedID, "1h", now)
	runtimeConfig, err := a.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig.Collector = cfg.Collector
	runtimeConfig.Collector.Enabled, runtimeConfig.Collector.PollIntervalSeconds, runtimeConfig.Collector.RetryIntervalSeconds = true, .2, 1
	if err := a.saveConfig(ctx, runtimeConfig); err != nil {
		t.Fatal(err)
	}
	a.Close()
	restarted, err := NewWithOptions(ctx, NewOptions{Migrate: true, StartBackground: false})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	runner := NewCollectorRunner(restarted)
	runner.Start()
	defer runner.Stop()
	deadline := time.Now().Add(4 * time.Second)
	for {
		state, err := restarted.collectorState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		completed := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id = ? AND state = 'complete'`, chargedID)
		resolved := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id IN (?, ?) AND state = 'ready'`, expiredID, matchedID)
		if completed == 1 && resolved == 2 && state.LastError != nil && strings.Contains(*state.LastError, "404") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail recovery starved behind ready prefix: completed=%d resolved=%d state=%#v", completed, resolved, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.Stop()
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_records`); got != 1 {
		t.Fatalf("metadata-only recovery created unpriced usage: %d", got)
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges`); got != 1 {
		t.Fatalf("metadata-only recovery created/replayed quota: %d", got)
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE id < ? AND state = 'ready' AND quota_handled = 0`, chargedItem.ID); got != collectorTTLBatchSize {
		t.Fatalf("unpriced prefix was modified: %d", got)
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id = ? AND (payload_json != '' OR caller_scope IS NOT NULL OR usage_user_id IS NOT NULL)`, chargedID); got != 0 {
		t.Fatal("handled tail receipt retained its processing payload")
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id = ? AND json_extract(payload_json, '$.usage.CacheCreationMetadata.status') = 'missing' AND json_extract(payload_json, '$.usage.CacheCreationMetadata.ttl_source') = 'unknown'`, expiredID); got != 1 {
		t.Fatal("expired tail did not freeze explicit unknown metadata")
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_collector_inbox WHERE execution_id = ? AND json_extract(payload_json, '$.usage.CacheCreationTTL') = '1h' AND json_extract(payload_json, '$.usage.CacheCreationMetadata.ttl_source') = 'enforced_request'`, matchedID); got != 1 {
		t.Fatal("metadata tail did not preserve available 1h evidence")
	}
	f.mu.Lock()
	lookups, queues := f.calls, f.queueCalls
	f.mu.Unlock()
	if lookups != 2 || queues != 0 {
		t.Fatalf("unexpected metadata/queue IO during selector outage: %d/%d", lookups, queues)
	}
	// A reliable pricing snapshot restores the unchanged first-accounting path.
	for _, expected := range []int{collectorTTLBatchSize, 2, 0} {
		if completed, err := restarted.processCollectorUsageInbox(ctx, cfg, pricing, now); err != nil || completed != expected {
			t.Fatalf("reliable pricing recovery = %d/%v, want %d", completed, err, expected)
		}
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM usage_records`); got != collectorTTLBatchSize+3 {
		t.Fatalf("recovered usage count = %d", got)
	}
	if got := collectorTTLCount(t, restarted, `SELECT COUNT(*) FROM user_quota_charges`); got != collectorTTLBatchSize+3 {
		t.Fatalf("recovered quota count = %d", got)
	}
	expiredUsage, matchedUsage := collectorTTLRecord(t, restarted, expiredRaw), collectorTTLRecord(t, restarted, matchedRaw)
	assertCollectorTTLCharge(t, restarted, expiredUsage.ID, 2, false)
	assertCollectorTTLCharge(t, restarted, matchedUsage.ID, 8, false)
	if expiredUsage.CacheCreationMetadata.Source == nil || *expiredUsage.CacheCreationMetadata.Source != "unknown" ||
		matchedUsage.CacheCreationMetadata.Source == nil || *matchedUsage.CacheCreationMetadata.Source != "enforced_request" {
		t.Fatal("first accounting changed the frozen tail attribution sources")
	}
	var total float64
	if err := restarted.db.QueryRow(`SELECT SUM(amount_usd) FROM user_quota_charges`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if expected := float64(collectorTTLBatchSize+2)*8 + 2; total != expected {
		t.Fatalf("recovered charges = %g, want %g", total, expected)
	}
	user, err := restarted.getUser(ctx, owner)
	if err != nil || user.QuotaLifetimeUSD == nil || *user.QuotaLifetimeUSD != quota-total {
		t.Fatalf("recovered quota was charged more than once: %#v/%v", user, err)
	}
}
