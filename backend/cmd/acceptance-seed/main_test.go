package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestValidateAcceptanceIsolation(t *testing.T) {
	acceptanceDir := t.TempDir()
	dbDir := filepath.Join(acceptanceDir, "data", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "cpa_helper.sqlite3")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(acceptanceDir, acceptanceIsolationMarkerName)
	if err := os.WriteFile(markerPath, []byte(acceptanceIsolationMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateAcceptanceIsolation(acceptanceDir, dbPath); err != nil {
		t.Fatalf("validate isolated database: %v", err)
	}
	t.Chdir(acceptanceDir)
	for _, paths := range [][2]string{
		{".", filepath.Join("data", "db", "cpa_helper.sqlite3")},
		{acceptanceDir, filepath.Join("data", "db", "cpa_helper.sqlite3")},
		{".", dbPath},
	} {
		if err := validateAcceptanceIsolation(paths[0], paths[1]); err != nil {
			t.Errorf("validate isolated relative paths %q: %v", paths, err)
		}
	}
	externalDBPath := filepath.Join(t.TempDir(), "cpa_helper.sqlite3")
	if err := os.WriteFile(externalDBPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateAcceptanceIsolation(acceptanceDir, externalDBPath); err == nil || !strings.Contains(err.Error(), "supplied database path escapes") {
		t.Fatalf("validate external database error = %v, want path escape rejection", err)
	}
	wrongDBPath := filepath.Join(dbDir, "other.sqlite3")
	if err := os.WriteFile(wrongDBPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateAcceptanceIsolation(acceptanceDir, wrongDBPath); err == nil || !strings.Contains(err.Error(), "-db must be the isolated database") {
		t.Fatalf("validate wrong database error = %v, want exact database rejection", err)
	}
	if err := validateAcceptanceIsolation(t.TempDir(), dbPath); err == nil || !strings.Contains(err.Error(), "read acceptance isolation marker") {
		t.Fatalf("validate unmarked directory error = %v, want marker rejection", err)
	}
	if err := validateAcceptanceIsolation("", dbPath); err == nil {
		t.Fatal("validate missing acceptance directory succeeded")
	}
	if err := os.WriteFile(markerPath, []byte("wrong marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateAcceptanceIsolation(acceptanceDir, dbPath); err == nil {
		t.Fatal("validate incorrect marker succeeded")
	}
}

func TestValidateAcceptanceIsolationAllowsInternalDirectoryLink(t *testing.T) {
	acceptanceDir := t.TempDir()
	actualDataDir := filepath.Join(acceptanceDir, "isolated-data")
	if err := os.MkdirAll(filepath.Join(actualDataDir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	actualDBPath := filepath.Join(actualDataDir, "db", "cpa_helper.sqlite3")
	if err := os.WriteFile(actualDBPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acceptanceDir, acceptanceIsolationMarkerName), []byte(acceptanceIsolationMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	createTestDirectorySymlink(t, actualDataDir, filepath.Join(acceptanceDir, "data"))
	for _, dbPath := range []string{actualDBPath, filepath.Join(acceptanceDir, "data", "db", "cpa_helper.sqlite3")} {
		if err := validateAcceptanceIsolation(acceptanceDir, dbPath); err != nil {
			t.Fatalf("validate directory link inside the marked root: %v", err)
		}
	}
}

func TestAcceptanceSeedRejectsDirectoryEscapeBeforeMutation(t *testing.T) {
	testAcceptanceSeedRejectsDirectoryEscapeBeforeMutation(t, createTestDirectorySymlink)
}

func testAcceptanceSeedRejectsDirectoryEscapeBeforeMutation(t *testing.T, createLink func(*testing.T, string, string)) {
	t.Helper()
	for _, linkedDir := range []string{"data", filepath.Join("data", "db")} {
		for _, mode := range []string{"seed", "cleanup"} {
			t.Run(linkedDir+"/"+mode, func(t *testing.T) {
				acceptanceDir := t.TempDir()
				externalDir := t.TempDir()
				linkPath := filepath.Join(acceptanceDir, linkedDir)
				dbPath := filepath.Join(acceptanceDir, "data", "db", "cpa_helper.sqlite3")
				dbSuffix, err := filepath.Rel(linkPath, dbPath)
				if err != nil {
					t.Fatal(err)
				}
				externalDBPath := filepath.Join(externalDir, dbSuffix)
				if err := os.MkdirAll(filepath.Dir(externalDBPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(acceptanceDir, acceptanceIsolationMarkerName), []byte(acceptanceIsolationMarker), 0o600); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", externalDBPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`
					CREATE TABLE app_settings (id INTEGER PRIMARY KEY, cliaproxy_url TEXT, management_key TEXT, updated_at TEXT);
					INSERT INTO app_settings VALUES (1, 'original-url', 'original-key', 'original-time');
					CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password_hash TEXT, password_salt TEXT,
						is_admin BOOLEAN, is_super_admin BOOLEAN, nickname TEXT, must_change_password BOOLEAN, created_at TEXT, updated_at TEXT);
					INSERT INTO users (id, username, nickname) VALUES (1, 'seed-bot', 'Original seed user');
				`); err != nil {
					t.Fatal(err)
				}
				createLink(t, externalDir, linkPath)
				if _, err := os.Stat(dbPath); err != nil {
					t.Fatalf("read database through the directory link: %v", err)
				}
				if err := validateAcceptanceIsolation(acceptanceDir, dbPath); err == nil {
					t.Errorf("validate directory escape error = %v, want marked-root rejection", err)
				}

				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("rejected seed reached backend: %s %s", r.Method, r.URL.Path)
					http.Error(w, "seed must reject before HTTP", http.StatusInternalServerError)
				}))
				defer backend.Close()
				args := []string{"-acceptance-dir", acceptanceDir, "-db", dbPath, "-backend", backend.URL, "-cpa", backend.URL}
				if mode == "cleanup" {
					args = append(args, "-cleanup-only")
				}
				encodedArgs, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, executable, "-test.run=^TestAcceptanceSeedCommand$")
				command.Env = append(os.Environ(), "CPA_HELPER_ACCEPTANCE_SEED_TEST_ARGS="+string(encodedArgs))
				output, err := command.CombinedOutput()
				if err == nil || !strings.Contains(string(output), "refusing to mutate database") {
					t.Errorf("seed command error = %v, output = %s; want isolation rejection before database access", err, output)
				}

				var cpaURL, managementKey, updatedAt string
				if err := db.QueryRow(`SELECT cliaproxy_url, management_key, updated_at FROM app_settings WHERE id = 1`).Scan(&cpaURL, &managementKey, &updatedAt); err != nil {
					t.Fatal(err)
				}
				if cpaURL != "original-url" || managementKey != "original-key" || updatedAt != "original-time" {
					t.Error("seed changed configuration outside the acceptance root")
				}
				var originalUsers int
				if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = 1 AND username = 'seed-bot' AND nickname = 'Original seed user'`).Scan(&originalUsers); err != nil {
					t.Fatal(err)
				}
				if originalUsers != 1 {
					t.Error("seed removed or replaced the user outside the acceptance root")
				}
			})
		}
	}
}

func createTestDirectorySymlink(t *testing.T, targetPath, linkPath string) {
	t.Helper()
	if err := os.Symlink(targetPath, linkPath); err != nil {
		if errors.Is(err, os.ErrPermission) || (runtime.GOOS == "windows" && strings.Contains(strings.ToLower(err.Error()), "privilege")) {
			t.Skipf("directory symlink creation is not permitted: %v", err)
		}
		t.Fatalf("create directory symlink %q -> %q: %v", linkPath, targetPath, err)
	}
}

func TestAcceptanceSeedCommand(t *testing.T) {
	encodedArgs := os.Getenv("CPA_HELPER_ACCEPTANCE_SEED_TEST_ARGS")
	if encodedArgs == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(encodedArgs), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{os.Args[0]}, args...)
	flag.CommandLine = flag.NewFlagSet("acceptance-seed", flag.ExitOnError)
	main()
}

func TestValidateLoopbackURL(t *testing.T) {
	for _, test := range []struct {
		url     string
		wantErr bool
	}{
		{url: "http://127.0.0.1:18318"},
		{url: "http://localhost:18318"},
		{url: "http://[::1]:18318"},
		{url: "https://127.0.0.1"},
		{url: "http://192.168.1.10:18318", wantErr: true},
		{url: "http://example.com", wantErr: true},
		{url: "http://user:pass@127.0.0.1:18318", wantErr: true},
		{url: "/relative", wantErr: true},
	} {
		t.Run(test.url, func(t *testing.T) {
			err := validateLoopbackURL("-backend", test.url)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateLoopbackURL(%q) error = %v, wantErr %t", test.url, err, test.wantErr)
			}
		})
	}
}
