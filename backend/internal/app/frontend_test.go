package app

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDetectRepoRootFromPrefersProjectAncestor(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(root, "backend", "bin")
	executablePath := filepath.Join(cwd, "cpa-helper.exe")
	got, err := detectRepoRootFrom(cwd, executablePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("repo root = %q, want %q", got, root)
	}
}

func TestDetectRepoRootFromFallsBackToExecutableDir(t *testing.T) {
	cwd := t.TempDir()
	releaseDir := t.TempDir()
	executablePath := filepath.Join(releaseDir, "cpa-helper.exe")

	got, err := detectRepoRootFrom(cwd, executablePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != releaseDir {
		t.Fatalf("repo root = %q, want %q", got, releaseDir)
	}
}

func TestHandleSPAServesEmbeddedFrontendAsset(t *testing.T) {
	app := &App{frontendFS: fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html>embedded</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('embedded')")},
	}}

	req := httptest.NewRequest("GET", "http://example.com/assets/app.js", nil)
	recorder := httptest.NewRecorder()
	if err := app.handleSPA(recorder, req); err != nil {
		t.Fatal(err)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "console.log('embedded')") {
		t.Fatalf("body = %q", body)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", cacheControl)
	}
}

func TestHandleSPAFallsBackToEmbeddedIndex(t *testing.T) {
	app := &App{frontendFS: fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>embedded index</html>")},
	}}

	req := httptest.NewRequest("GET", "http://example.com/settings/account", nil)
	recorder := httptest.NewRecorder()
	if err := app.handleSPA(recorder, req); err != nil {
		t.Fatal(err)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "embedded index") {
		t.Fatalf("body = %q", body)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("index Cache-Control = %q", cacheControl)
	}
}

func TestHandleSPADoesNotReturnEmbeddedIndexForMissingAsset(t *testing.T) {
	app := &App{frontendFS: fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>embedded index</html>")},
	}}

	req := httptest.NewRequest("GET", "http://example.com/assets/stale-chunk.js", nil)
	recorder := httptest.NewRecorder()
	err := app.handleSPA(recorder, req)
	appErr, ok := err.(*AppError)
	if !ok || appErr.Status != 404 {
		t.Fatalf("handleSPA error = %#v, want 404 AppError", err)
	}
	if strings.Contains(recorder.Body.String(), "embedded index") {
		t.Fatalf("missing asset body = %q, must not contain index", recorder.Body.String())
	}
}

func TestHandleSPAFrontendDistOverrideUsesExternalFiles(t *testing.T) {
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<html>external</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{
		frontendDist: distDir,
		frontendEnv:  true,
		frontendFS: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("<html>embedded</html>")},
		},
	}

	req := httptest.NewRequest("GET", "http://example.com/", nil)
	recorder := httptest.NewRecorder()
	if err := app.handleSPA(recorder, req); err != nil {
		t.Fatal(err)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "external") || strings.Contains(body, "embedded") {
		t.Fatalf("body = %q", body)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("external index Cache-Control = %q", cacheControl)
	}
}

func TestHandleSPAServesExternalFrontendAssetWithImmutableCache(t *testing.T) {
	distDir := t.TempDir()
	assetsDir := filepath.Join(distDir, "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "app-hash.js"), []byte("console.log('external')"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{frontendDist: distDir, frontendEnv: true}

	req := httptest.NewRequest("GET", "http://example.com/assets/app-hash.js", nil)
	recorder := httptest.NewRecorder()
	if err := app.handleSPA(recorder, req); err != nil {
		t.Fatal(err)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "public, max-age=31536000, immutable" {
		t.Fatalf("external asset Cache-Control = %q", cacheControl)
	}
}

func TestHandleSPADoesNotReturnExternalIndexForMissingAsset(t *testing.T) {
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<html>external</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{frontendDist: distDir, frontendEnv: true}

	req := httptest.NewRequest("GET", "http://example.com/assets/stale-chunk.js", nil)
	recorder := httptest.NewRecorder()
	err := app.handleSPA(recorder, req)
	appErr, ok := err.(*AppError)
	if !ok || appErr.Status != 404 {
		t.Fatalf("handleSPA error = %#v, want 404 AppError", err)
	}
	if strings.Contains(recorder.Body.String(), "external") {
		t.Fatalf("missing asset body = %q, must not contain index", recorder.Body.String())
	}
}

func TestModelMonitorViewUsesIndependentSourceRefresh(t *testing.T) {
	path := filepath.Join("..", "..", "..", "frontend", "src", "features", "model-monitor", "views", "ModelMonitorView.vue")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, expected := range []string{
		"getModelMonitorSettings",
		"getModelMonitorSource",
		"updateModelMonitorSettings",
		"const sourceStates = reactive<Record<string, ModelMonitorSourceViewState>>({})",
		"async function refreshSource(id: string)",
		"function refreshAllSources(): void",
		"async function loadModelMonitorSettings(): Promise<void>",
		"const settings = await getModelMonitorSettings()",
		"const source = await getModelMonitorSource(id)",
		`@click="refreshAllSources"`,
		`@click="refreshSource(slot.definition.id)"`,
		"模型监控来源配置",
		"enabled_source_ids:",
		"const saved = await updateModelMonitorSettings(payload)",
		"t('重试', 'Retry')",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("ModelMonitorView.vue missing independent source-refresh contract %q", expected)
		}
	}
	if count := strings.Count(source, "!slot.state.error &&"); count != 2 {
		t.Fatalf("ModelMonitorView.vue checks source errors in %d health totals, want 2", count)
	}
	for _, forbidden := range []string{
		"monitorRequestPending",
		"monitorReloadQueued",
		"getModelMonitor()",
		"loadMonitor(true)",
		"t('内置', 'Built-in')",
		`<div v-else-if="source.services.length" class="service-list">`,
		`<NCollapse v-else-if="source.groups.length" class="group-list">`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("ModelMonitorView.vue still contains removed aggregate-refresh behavior %q", forbidden)
		}
	}
	for _, expected := range []string{
		"<NButton secondary @click=\"openSourceSettings\">",
		"<NButton secondary @click=\"openProxySettings\">",
		"<NButton secondary :loading=\"refreshing\"",
		"arrow-placement=\"right\"",
		".service-list + .group-list { margin-top: 18px; padding-top: 12px; border-top: 1px solid var(--cpa-border); }",
		".group-services { display: grid; gap: 8px; padding: 2px 0 8px 30px; }",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("ModelMonitorView.vue missing unified header action style %q", expected)
		}
	}
	assertOrdered := func(label string, expected ...string) {
		t.Helper()
		offset := 0
		for _, part := range expected {
			index := strings.Index(source[offset:], part)
			if index < 0 {
				t.Fatalf("ModelMonitorView.vue missing ordered %s contract %q", label, part)
			}
			offset += index + len(part)
		}
	}
	assertOrdered(
		"per-source queue",
		"if (state.pending) {",
		"state.queued = true",
		"state.pending = true",
		"const source = await getModelMonitorSource(id)",
		"state.pending = false",
		"if (state.queued && isSourceEnabled(id))",
		"void refreshSource(id)",
	)
	assertOrdered(
		"source collection-error retention",
		"if (source.collection_state === 'error' && state.source?.collection_state === 'ok') {",
		"state.error = serverText(source.collection_error ?? '', '本次采集失败', 'This collection failed')",
		"} else {",
		"state.source = source",
	)
	assertOrdered(
		"source health error priority",
		"function sourceSlotHealthIcon(slot: ModelMonitorSourceSlot)",
		"if (slot.state.error) return WifiOff",
		"if (!slot.state.source) return RefreshCw",
	)
	assertOrdered(
		"settings then fan-out",
		"const settings = await getModelMonitorSettings()",
		"applyModelMonitorSettings(settings)",
		"refreshAllSources()",
	)
	assertOrdered(
		"source-settings close bootstrap recovery",
		"watch(sourceSettingsModalOpen, (open) => {",
		"if (open) return",
		"const bootstrapPending = settingsLoading.value && !modelMonitorSettings.value",
		"sourceSettingsModalGeneration++",
		"if (bootstrapPending) void loadModelMonitorSettings()",
	)
	assertOrdered(
		"settings error render priority",
		`<div v-if="enabledSourceSlots.length" class="source-grid">`,
		`v-else-if="!settingsLoading && settingsLoadError"`,
		`@click="loadModelMonitorSettings"`,
		"t('重试', 'Retry')",
		`v-else-if="!settingsLoading"`,
	)
	assertOrdered(
		"top-level services before groups",
		`<div v-if="source.services.length" class="service-list">`,
		`<NCollapse v-if="source.groups.length" class="group-list" arrow-placement="right">`,
	)
}
