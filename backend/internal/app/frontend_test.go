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

func TestModelMonitorViewKeepsAggregateLoadErrorsVisible(t *testing.T) {
	path := filepath.Join("..", "..", "..", "frontend", "src", "features", "model-monitor", "views", "ModelMonitorView.vue")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, expected := range []string{
		"const loading = ref(true)",
		"if (sources.value.length === 0) {",
		"aggregateLoadError.value = localizedError",
		"aggregateLoadError.value = null",
		`@click="loadMonitor(true)"`,
		"t('重试', 'Retry')",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("ModelMonitorView.vue missing aggregate load state contract %q", expected)
		}
	}
	if count := strings.Count(source, "sources.value ="); count != 1 {
		t.Fatalf("ModelMonitorView.vue assigns sources %d times, want only the successful response assignment", count)
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
		"request state",
		"if (monitorRequestPending) {",
		"if (manual) monitorReloadQueued = true",
		"monitorRequestPending = true",
		"const response = await getModelMonitor()",
		"sources.value = response.sources",
		"aggregateLoadError.value = null",
		"} catch (error) {",
		"aggregateLoadError.value = localizedError",
		"} finally {",
		"monitorRequestPending = false",
		"if (monitorReloadQueued) {",
		"void loadMonitor(true)",
	)
	assertOrdered(
		"render priority",
		`<div v-if="sources.length" class="source-grid">`,
		`v-else-if="!loading && aggregateLoadError"`,
		`@click="loadMonitor(true)"`,
		"t('重试', 'Retry')",
		`v-else-if="!loading && !aggregateLoadError"`,
	)
}
