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
