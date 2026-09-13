package app

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestUsageSourceOptionsNamedUnpricedHistory(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	source, authIndex := "sk-options-history-original-123456", "History-Auth.json"
	hash := hashAPIKey(source)
	installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{{
		Brand: aiProviderBrandGemini, AuthIndex: &authIndex, APIKeyHash: &hash,
		Models: []aiProviderModel{{Name: "history-model"}},
	}})
	if _, err := a.db.Exec(`INSERT INTO model_price_channel_aliases
		(auth_type, channel_brand, channel_key, label, created_at, updated_at)
		VALUES ('apikey', 'gemini', ?, 'History channel', '', '')`, authIndex); err != nil {
		t.Fatal(err)
	}
	start := time.Now().In(appTimeLocation).AddDate(0, 0, -30).Truncate(time.Hour)
	end := start.Add(time.Hour)
	if _, err := a.db.Exec(`INSERT INTO usage_records
		(created_at, timestamp, provider, model, source, auth, auth_index, input_tokens,
		 total_tokens, dedupe_key, raw_json)
		VALUES (?, ?, 'gemini', 'history-model', ?, 'API-KEY', ?, 10, 10,
		 'source-options-history', '{"auth_type":"api_key"}')`, dbTime(start), dbTime(start), source, authIndex); err != nil {
		t.Fatal(err)
	}
	filters := UsageFilters{Scope: "admin", Start: &start, End: &end}
	want := []map[string]string{{
		"key": *usageSourceKey(&source), "label": "History channel · " + maskSecret(&source),
	}}
	assertOptions := func() {
		t.Helper()
		response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, filters)
		if err != nil {
			t.Fatal(err)
		}
		if got := response["sources"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("named historical source options = %#v, want %#v", got, want)
		}
	}
	assertOptions()
	if pruned, err := a.pruneExpiredUsageRawJSON(ctx, time.Now()); err != nil || pruned != 1 {
		t.Fatalf("prune historical raw payload: count=%d error=%v", pruned, err)
	}
	assertOptions()
	var raw string
	if err := a.db.QueryRow(`SELECT raw_json FROM usage_records WHERE dedupe_key = 'source-options-history'`).Scan(&raw); err != nil || raw != "" {
		t.Fatalf("historical raw payload = %q, error=%v", raw, err)
	}
	if _, err := a.db.Exec(`UPDATE model_price_channel_aliases SET label = ?`, maskSecret(&source)); err != nil {
		t.Fatal(err)
	}
	want[0]["label"] = maskSecret(&source)
	assertOptions()
	// Names do not depend on a price record or even a price-table read.
	if _, err := a.db.Exec(`DROP TABLE model_prices`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE model_price_channel_aliases SET label = 'History channel'`); err != nil {
		t.Fatal(err)
	}
	want[0]["label"] = "History channel · " + maskSecret(&source)
	assertOptions()
}

func installUsageSourceOptionsSnapshot(t *testing.T, a *App, providers []aiProviderItem) {
	t.Helper()
	const configKey = "usage-source-options-test"
	a.priceSelectors.retainConfig(configKey)
	generation, ok := a.priceSelectors.currentGeneration(configKey)
	if !ok || !a.priceSelectors.storeWithLabels(configKey, generation, time.Now(),
		modelPriceChannelSelectors(providers), modelPriceChannelLabels(providers), usageSourceChannels(providers)) {
		t.Fatal("could not install source options selector snapshot")
	}
}

func TestUsageSourceOptionsRequireConsistentIdentity(t *testing.T) {
	const originalSource = "sk-options-identity-original-123456"
	native := func(brand aiProviderBrand, index, source string, models ...string) aiProviderItem {
		hash := hashAPIKey(source)
		provider := aiProviderItem{Brand: brand, AuthIndex: &index, APIKeyHash: &hash}
		for _, model := range models {
			provider.Models = append(provider.Models, aiProviderModel{Name: model})
		}
		return provider
	}
	compatible := func(name string, models ...string) aiProviderItem {
		provider := aiProviderItem{Brand: aiProviderBrandOpenAICompatibility, Name: &name}
		for _, model := range models {
			provider.Models = append(provider.Models, aiProviderModel{Name: model})
		}
		return provider
	}
	alias := func(brand, key, label string) modelPriceChannelAlias {
		return modelPriceChannelAlias{AuthType: "apikey", ChannelBrand: brand, ChannelKey: key, Label: label}
	}
	type evidence struct{ provider, model, index string }
	base := evidence{"gemini", "one", "Auth.json"}
	googleOne, googleTwo := evidence{"google", "one", "Auth.json"}, evidence{"google", "two", "Auth.json"}
	noIndex := evidence{"gemini", "one", ""}
	keyhash := "keyhash:" + hashAPIKey(originalSource)
	nativeAlias := alias("gemini", "Auth.json", "Shared name")
	compatibleAlias := alias("openai_compatibility", "gemini", "Compatible name")
	cases := []struct {
		name      string
		providers []aiProviderItem
		aliases   []modelPriceChannelAlias
		rows      []evidence
		source    string
		wantName  string
	}{
		{
			name:      "same identity across models",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one", "two")},
			aliases:   []modelPriceChannelAlias{nativeAlias}, rows: []evidence{googleOne, googleTwo}, wantName: "Shared name",
		},
		{
			name:      "same name across different brands",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one"), native(aiProviderBrandVertex, "Auth.json", originalSource, "two")},
			aliases:   []modelPriceChannelAlias{nativeAlias, alias("vertex", "Auth.json", "Shared name")}, rows: []evidence{googleOne, googleTwo},
		},
		{
			name:      "catalog first index cannot represent later identity",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one"), native(aiProviderBrandGemini, "Second.json", originalSource, "two")},
			aliases:   []modelPriceChannelAlias{nativeAlias, alias("gemini", "Second.json", "Shared name")}, rows: []evidence{base, {"gemini", "two", "Second.json"}},
		},
		{
			name:      "reversed indexes cannot select first identity",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one"), native(aiProviderBrandGemini, "Second.json", originalSource, "two")},
			aliases:   []modelPriceChannelAlias{nativeAlias, alias("gemini", "Second.json", "Shared name")}, rows: []evidence{{"gemini", "two", "Second.json"}, base},
		},
		{
			name:      "native selector case distinguishes identities",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one"), native(aiProviderBrandGemini, "auth.json", originalSource, "two")},
			aliases:   []modelPriceChannelAlias{nativeAlias, alias("gemini", "auth.json", "Shared name")}, rows: []evidence{base, {"gemini", "two", "auth.json"}},
		},
		{
			name:      "unresolved evidence before match",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias}, rows: []evidence{{"unknown-provider", "one", "Auth.json"}, base},
		},
		{
			name:      "unresolved evidence after match",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias}, rows: []evidence{base, {"unknown-provider", "one", "Auth.json"}},
		},
		{
			name:      "model conflict cannot be replaced by another model match",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one", "two"), native(aiProviderBrandVertex, "Auth.json", originalSource, "two")},
			aliases:   []modelPriceChannelAlias{nativeAlias, alias("vertex", "Auth.json", "Shared name")}, rows: []evidence{googleTwo, googleOne},
		},
		{
			name:      "native credentials survive model removal before compatible alias",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "other"), compatible("gemini", "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias, compatibleAlias}, rows: []evidence{base},
		},
		{
			name:      "native credentials survive before compatible upstream name",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "other"), compatible("gemini", "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias}, rows: []evidence{base},
		},
		{
			name:      "keyhash credentials survive before compatible alias",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "", originalSource, "other"), compatible("gemini", "one")},
			aliases:   []modelPriceChannelAlias{alias("gemini", keyhash, "No index"), compatibleAlias}, rows: []evidence{noIndex},
		},
		{
			name:      "duplicate identity cannot use first matching hash",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one"), native(aiProviderBrandGemini, "Auth.json", "other-key", "other")},
			aliases:   []modelPriceChannelAlias{nativeAlias}, rows: []evidence{base},
		},
		{
			name:      "duplicate identity cannot use reversed matching hash",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "other-key", "other"), native(aiProviderBrandGemini, "Auth.json", originalSource, "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias}, rows: []evidence{base},
		},
		{
			name:      "duplicate native identity prevents compatible model selection",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "other-key", "other"), native(aiProviderBrandGemini, "Auth.json", originalSource, "other"), compatible("gemini", "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias, compatibleAlias}, rows: []evidence{base},
		},
		{
			name:      "different full credential allows compatible model selection",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", "other-key", "other"), compatible("gemini", "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias, compatibleAlias}, rows: []evidence{base}, wantName: "gemini",
		},
		{
			name:      "masked source is not surviving native credential evidence",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "", originalSource, "other"), compatible("gemini", "one")},
			aliases:   []modelPriceChannelAlias{compatibleAlias}, rows: []evidence{noIndex}, source: "sk-opt...3456", wantName: "gemini",
		},
		{
			name:      "missing auth index is not inferred",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "Auth.json", originalSource, "one")},
			aliases:   []modelPriceChannelAlias{nativeAlias}, rows: []evidence{noIndex},
		},
		{
			name:      "exact no-index credential permits keyhash alias",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "", originalSource, "one")},
			aliases:   []modelPriceChannelAlias{alias("gemini", keyhash, "No index")}, rows: []evidence{noIndex}, wantName: "No index",
		},
		{
			name:      "masked source cannot resolve keyhash alias",
			providers: []aiProviderItem{native(aiProviderBrandGemini, "", originalSource, "one")},
			aliases:   []modelPriceChannelAlias{alias("gemini", keyhash, "No index")}, rows: []evidence{noIndex}, source: "sk-opt...3456",
		},
		{
			name:    "retained exact alias after provider removal",
			aliases: []modelPriceChannelAlias{nativeAlias}, rows: []evidence{base}, wantName: "Shared name",
		},
		{
			name:    "retained keyhash needs current credential",
			aliases: []modelPriceChannelAlias{alias("gemini", keyhash, "No index")}, rows: []evidence{noIndex},
		},
		{
			name:      "compatible runtime prefix ignores legacy canonical alias",
			providers: []aiProviderItem{compatible("Fixture Vendor", "one", "two")},
			aliases:   []modelPriceChannelAlias{alias("openai_compatibility", "FIXTURE VENDOR", "Shared name")},
			rows:      []evidence{{"openai-compatible-fixture vendor", "one", "first-key"}, {"FIXTURE VENDOR", "two", "second-key"}}, wantName: "Fixture Vendor",
		},
		{
			name:      "duplicate compatible identity despite different models",
			providers: []aiProviderItem{compatible("Fixture Vendor", "one"), compatible("fixture vendor", "two")},
			aliases:   []modelPriceChannelAlias{alias("openai_compatibility", "fixture vendor", "Shared name")},
			rows:      []evidence{{"openai-compatible-fixture vendor", "one", "first-key"}},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
			ctx := context.Background()
			a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			installUsageSourceOptionsSnapshot(t, a, test.providers)
			for _, item := range test.aliases {
				if _, err := a.db.Exec(`INSERT INTO model_price_channel_aliases
					(auth_type, channel_brand, channel_key, label, created_at, updated_at)
					VALUES (?, ?, ?, ?, '', '')`, item.AuthType, item.ChannelBrand,
					canonicalModelPriceChannelKey(item.ChannelBrand, item.ChannelKey), item.Label); err != nil {
					t.Fatal(err)
				}
			}
			source := test.source
			if source == "" {
				source = originalSource
			}
			start := time.Now().In(appTimeLocation).AddDate(0, 0, -30).Truncate(time.Hour)
			end := start.Add(time.Hour)
			for i, row := range test.rows {
				insertUsageSourceOptionsRecord(t, a, source, row.provider, row.model, row.index, start.Add(time.Duration(i)*time.Minute))
			}
			if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
				t.Fatal(err)
			}
			if pruned, err := a.pruneExpiredUsageRawJSON(ctx, time.Now()); err != nil || pruned != int64(len(test.rows)) {
				t.Fatalf("prune source evidence: count=%d error=%v", pruned, err)
			}
			response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, UsageFilters{Scope: "admin", Start: &start, End: &end})
			if err != nil {
				t.Fatal(err)
			}
			label := maskSecret(&source)
			if test.wantName != "" {
				label = test.wantName + " · " + label
			}
			want := []map[string]string{{"key": *usageSourceKey(&source), "label": label}}
			if got := response["sources"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("source identity options = %#v, want %#v", got, want)
			}
		})
	}
}

func TestUsageSourceOptionsKeepDistinctKeysWithEqualLabels(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	name := "Same channel name"
	installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{{
		Brand: aiProviderBrandOpenAICompatibility, Name: &name, Models: []aiProviderModel{{Name: "one"}},
	}})
	start := time.Now().In(appTimeLocation).Truncate(time.Hour)
	end := start.Add(time.Hour)
	sources := []string{"sk-same-first-credential-1234", "sk-same-other-credential-1234"}
	want := make([]map[string]string, 0, len(sources))
	for i, source := range sources {
		insertUsageSourceOptionsRecord(t, a, source, "openai-compatible-same channel name", "one", "", start.Add(time.Duration(i)*time.Minute))
		want = append(want, map[string]string{"key": *usageSourceKey(&source), "label": name + " · " + maskSecret(&source)})
	}
	if want[0]["label"] != want[1]["label"] || want[0]["key"] == want[1]["key"] {
		t.Fatal("fixture must contain equal visible names/masks and different source keys")
	}
	sort.Slice(want, func(i, j int) bool { return want[i]["key"] < want[j]["key"] })
	response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, UsageFilters{Scope: "admin", Start: &start, End: &end})
	if err != nil {
		t.Fatal(err)
	}
	if got := response["sources"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("same-label source options = %#v, want distinct sorted keys %#v", got, want)
	}
}

func TestUsageSourceOptionsRequireAvailableSelectorSnapshot(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	name, source := "Snapshot vendor", "sk-options-snapshot-original-123456"
	providers := []aiProviderItem{{Brand: aiProviderBrandOpenAICompatibility, Name: &name}}
	start := time.Now().In(appTimeLocation).Truncate(time.Hour)
	end := start.Add(time.Hour)
	insertUsageSourceOptionsRecord(t, a, source, name, "one", "", start)
	assertLabel := func(t *testing.T, label string) {
		t.Helper()
		response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, UsageFilters{Scope: "admin", Start: &start, End: &end})
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]string{{"key": *usageSourceKey(&source), "label": label}}
		if got := response["sources"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot source options = %#v, want %#v", got, want)
		}
	}
	const configKey = "usage-source-options-test"
	for _, test := range []struct {
		name   string
		change func(*testing.T)
	}{
		{"missing snapshot", func(t *testing.T) { a.priceSelectors.invalidate(configKey) }},
		{"expired snapshot", func(t *testing.T) {
			a.priceSelectors.mu.Lock()
			a.priceSelectors.expiresAt = time.Now().Add(-time.Second)
			a.priceSelectors.mu.Unlock()
		}},
		{"old configuration cannot repopulate snapshot", func(t *testing.T) {
			generation, _ := a.priceSelectors.currentGeneration(configKey)
			a.priceSelectors.retainConfig("changed-source-options-config")
			if a.priceSelectors.storeWithLabels(configKey, generation, time.Now(), modelPriceChannelSelectors(providers), modelPriceChannelLabels(providers), usageSourceChannels(providers)) {
				t.Fatal("old config repopulated selector snapshot")
			}
		}},
		{"old generation cannot repopulate snapshot", func(t *testing.T) {
			generation, _ := a.priceSelectors.currentGeneration(configKey)
			a.priceSelectors.invalidate(configKey)
			if a.priceSelectors.storeWithLabels(configKey, generation, time.Now(), modelPriceChannelSelectors(providers), modelPriceChannelLabels(providers), usageSourceChannels(providers)) {
				t.Fatal("old generation repopulated selector snapshot")
			}
		}},
		{"snapshot without source identities", func(t *testing.T) {
			generation, _ := a.priceSelectors.currentGeneration(configKey)
			if !a.priceSelectors.storeWithLabels(configKey, generation, time.Now(), modelPriceChannelSelectors(providers), modelPriceChannelLabels(providers), nil) {
				t.Fatal("could not install selector-only snapshot")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			installUsageSourceOptionsSnapshot(t, a, providers)
			assertLabel(t, name+" · "+maskSecret(&source))
			test.change(t)
			assertLabel(t, maskSecret(&source))
		})
	}
}

func TestUsageSourceOptionsKeepPermanentAuthConflictOutsideRange(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	name, source := "Auth conflict vendor", "sk-options-auth-original-123456"
	installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{{Brand: aiProviderBrandOpenAICompatibility, Name: &name}})
	start := time.Now().In(appTimeLocation).AddDate(0, 0, -30).Truncate(time.Hour)
	end := start.Add(time.Hour)
	insertUsageSourceOptionsRecord(t, a, source, name, "one", "first-index", start)
	filters := UsageFilters{Scope: "admin", Start: &start, End: &end}
	assertLabel := func(label string) {
		t.Helper()
		response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, filters)
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]string{{"key": *usageSourceKey(&source), "label": label}}
		if got := response["sources"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("auth-conflict source options = %#v, want %#v", got, want)
		}
	}
	assertLabel(name + " · " + maskSecret(&source))
	conflictID := insertUsageSourceOptionsRecord(t, a, source, name, "one", "second-index", end.Add(time.Hour))
	if _, err := a.db.Exec(`UPDATE usage_records SET auth = 'oauth', raw_json = '{"auth_type":"oauth"}' WHERE id = ?`, conflictID); err != nil {
		t.Fatal(err)
	}
	assertLabel(maskSecret(&source))
	// Later successful API-key evidence and raw pruning must not erase an
	// authentication conflict outside the selected date range.
	insertUsageSourceOptionsRecord(t, a, source, name, "one", "first-index", start.Add(time.Minute))
	assertLabel(maskSecret(&source))
	if pruned, err := a.pruneExpiredUsageRawJSON(ctx, time.Now()); err != nil || pruned != 3 {
		t.Fatalf("prune mixed-auth history: count=%d error=%v", pruned, err)
	}
	assertLabel(maskSecret(&source))
	var conflict bool
	if err := a.db.QueryRow(`SELECT auth_conflict FROM usage_source_catalog WHERE source_key = ?`, *usageSourceKey(&source)).Scan(&conflict); err != nil || !conflict {
		t.Fatalf("persistent source auth conflict = %v, error=%v", conflict, err)
	}
}

func TestUsageSourceOptionsRejectInconsistentFactAuthentication(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	ctx := context.Background()
	a, err := NewWithOptions(ctx, NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	name, source := "Fact auth vendor", "sk-options-fact-auth-original-123456"
	installUsageSourceOptionsSnapshot(t, a, []aiProviderItem{{Brand: aiProviderBrandOpenAICompatibility, Name: &name}})
	start := time.Now().In(appTimeLocation).Truncate(time.Hour)
	end := start.Add(time.Hour)
	id := insertUsageSourceOptionsRecord(t, a, source, name, "one", "first-index", start)
	insertUsageSourceOptionsRecord(t, a, source, name, "two", "second-index", start.Add(time.Minute))
	if err := a.ensureUsageAnalyticsFacts(ctx); err != nil {
		t.Fatal(err)
	}
	for _, auth := range []any{nil, "oauth"} {
		if _, err := a.db.Exec(`UPDATE usage_analytics_facts SET auth = ? WHERE usage_record_id = ?`, auth, id); err != nil {
			t.Fatal(err)
		}
		response, err := a.usageOptionsResponse(ctx, &AuthUser{IsAdmin: true}, UsageFilters{Scope: "admin", Start: &start, End: &end})
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]string{{"key": *usageSourceKey(&source), "label": maskSecret(&source)}}
		if got := response["sources"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("fact auth %v source options = %#v, want %#v", auth, got, want)
		}
	}
}

func insertUsageSourceOptionsRecord(t *testing.T, a *App, source, provider, model, authIndex string, timestamp time.Time) int64 {
	t.Helper()
	result, err := a.db.Exec(`INSERT INTO usage_records
		(created_at, timestamp, provider, model, source, auth, auth_index, input_tokens,
		 total_tokens, dedupe_key, raw_json)
		VALUES (?, ?, ?, ?, ?, 'api_key', ?, 10, 10, ?, '{"auth_type":"api_key"}')`,
		dbTime(timestamp), dbTime(timestamp), provider, model, source, authIndex, fmt.Sprintf("%s/%s/%s", provider, source, timestamp))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
