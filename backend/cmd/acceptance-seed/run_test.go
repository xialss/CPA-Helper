package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	backendApp "cpa-helper/backend/internal/app"
)

type seedTestRuntime struct {
	root   string
	dbPath string
	db     *sql.DB
	server *httptest.Server
	admin  *client
}

func newSeedTestRuntime(t *testing.T, intercept func(*sql.DB, http.ResponseWriter, *http.Request) bool) seedTestRuntime {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	if err := os.WriteFile(filepath.Join(root, acceptanceIsolationMarkerName), []byte(acceptanceIsolationMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	application, err := backendApp.NewWithOptions(t.Context(), backendApp.NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)
	dbPath := filepath.Join(dataDir, "db", "cpa_helper.sqlite3")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	handler := application.Routes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if intercept != nil && intercept(db, w, r) {
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := &client{base: server.URL, http: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	if err := admin.do(http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "acceptance-owner", "password": "acceptance-test-password", "nickname": "Acceptance Owner",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (username, is_admin, is_super_admin, disabled_at, created_at, updated_at)
		VALUES ('existing-admin', 1, 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return seedTestRuntime{root: root, dbPath: dbPath, db: db, server: server, admin: admin}
}

func runSeedTestCommand(t *testing.T, runtime seedTestRuntime, cpaURL string, extraArgs ...string) (string, error) {
	t.Helper()
	args := []string{"-acceptance-dir", runtime.root, "-db", runtime.dbPath, "-backend", runtime.server.URL, "-cpa", cpaURL}
	args = append(args, extraArgs...)
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
	if ctx.Err() != nil {
		t.Fatalf("seed command did not complete: %v: %s", ctx.Err(), output)
	}
	return string(output), err
}

func assertSeedTestCleanup(t *testing.T, runtime seedTestRuntime, seedCookie string) {
	t.Helper()
	var seedUsers, seedKeys int
	if err := runtime.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, seedUsername).Scan(&seedUsers); err != nil {
		t.Fatal(err)
	}
	if err := runtime.db.QueryRow(`SELECT COUNT(*) FROM user_api_keys WHERE api_key_hash = 'seed-cleanup-test'`).Scan(&seedKeys); err != nil {
		t.Fatal(err)
	}
	if seedUsers != 0 || seedKeys != 0 {
		t.Errorf("temporary seed users/keys = %d/%d, want 0/0", seedUsers, seedKeys)
	}
	var unchangedUsers int
	if err := runtime.db.QueryRow(`SELECT COUNT(*) FROM users WHERE
		(username = 'acceptance-owner' AND is_admin = 1 AND is_super_admin = 1 AND disabled_at IS NULL) OR
		(username = 'existing-admin' AND is_admin = 1 AND is_super_admin = 0 AND disabled_at = '2026-01-01T00:00:00Z')`).Scan(&unchangedUsers); err != nil {
		t.Fatal(err)
	}
	if unchangedUsers != 2 {
		t.Errorf("unchanged existing users = %d, want 2", unchangedUsers)
	}
	if seedCookie != "" {
		request, err := http.NewRequest(http.MethodGet, runtime.server.URL+"/api/auth/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Cookie", seedCookie)
		response, err := runtime.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("deleted seed session status = %d, want 401", response.StatusCode)
		}
	}
}

func seedTestRelatedKey(db *sql.DB) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO user_api_keys (api_key_hash, user_id, created_at, updated_at)
		SELECT 'seed-cleanup-test', id, created_at, updated_at FROM users WHERE username = ?`, seedUsername)
	return err
}

func newSeedTestCPA(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-management-key" {
			http.Error(w, "unexpected management identity", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/openai-compatibility":
			json.NewEncoder(w).Encode([]map[string]any{{
				"name": "deepseek", "base-url": "http://127.0.0.1/v1", "api-key-entries": []any{},
				"models": []map[string]string{{"name": "deepseek-v4-flash"}},
			}})
		case "/v0/management/gemini-api-key", "/v0/management/codex-api-key", "/v0/management/claude-api-key",
			"/v0/management/vertex-api-key", "/v0/management/xai-api-key", "/v0/management/api-key-usage":
			json.NewEncoder(w).Encode([]any{})
		default:
			t.Errorf("unexpected fake CPA request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAcceptanceSeedCleansUserAfterFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		scope string
	}{
		{name: "login", path: "/api/auth/login"},
		{name: "settings", path: "/api/settings"},
		{name: "library_import", path: "/api/model-prices", scope: "library"},
		{name: "channel_import", path: "/api/model-prices", scope: "channel"},
		{name: "provider_snapshot", path: "/api/ai-providers"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var seedCookie atomic.Value
			runtime := newSeedTestRuntime(t, func(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != test.path {
					return false
				}
				if test.scope != "" {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read price request: %v", err)
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					var payload struct {
						Scope string `json:"price_scope"`
					}
					if err := json.Unmarshal(body, &payload); err != nil {
						t.Errorf("decode price request: %v", err)
					}
					if payload.Scope != test.scope {
						return false
					}
				}
				seedCookie.Store(r.Header.Get("Cookie"))
				if err := seedTestRelatedKey(db); err != nil {
					t.Errorf("create related seed key: %v", err)
				}
				http.Error(w, "injected "+test.name+" failure", http.StatusInternalServerError)
				return true
			})
			cpa := newSeedTestCPA(t)
			output, err := runSeedTestCommand(t, runtime, cpa.URL)
			if err == nil || !strings.Contains(output, "injected "+test.name+" failure") {
				t.Errorf("seed error = %v, output = %s; want original failure", err, output)
			}
			cookie, _ := seedCookie.Load().(string)
			assertSeedTestCleanup(t, runtime, cookie)
		})
	}
}

func TestAcceptanceSeedUpdatesRunningSelectorIdentity(t *testing.T) {
	var settingsCalls, seedPriceCalls atomic.Int64
	var seedCookie atomic.Value
	runtime := newSeedTestRuntime(t, func(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/settings" && r.URL.Path != "/api/model-prices" {
			return false
		}
		var superAdmin bool
		if err := db.QueryRow(`SELECT is_super_admin FROM users WHERE username = ?`, seedUsername).Scan(&superAdmin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false
			}
			t.Errorf("read temporary seed role: %v", err)
			return false
		}
		if r.URL.Path == "/api/settings" {
			settingsCalls.Add(1)
			if !superAdmin {
				t.Error("seed settings save requires its temporary super-admin role")
			}
		} else {
			seedPriceCalls.Add(1)
			seedCookie.Store(r.Header.Get("Cookie"))
			if superAdmin {
				t.Error("seed must drop its super-admin role before importing prices")
			}
			if err := seedTestRelatedKey(db); err != nil {
				t.Errorf("create related seed key: %v", err)
			}
		}
		return false
	})
	initialCPA := newSeedTestCPA(t)
	if err := runtime.admin.do(http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url": initialCPA.URL, "management_key": "fake-management-key",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.admin.do(http.MethodGet, "/api/ai-providers", nil, nil); err != nil {
		t.Fatal(err)
	}
	cpa := newSeedTestCPA(t)
	for iteration := 1; iteration <= 2; iteration++ {
		output, err := runSeedTestCommand(t, runtime, cpa.URL)
		if err != nil {
			t.Fatalf("seed pass %d: %v: %s", iteration, err, output)
		}
		if settingsCalls.Load() != int64(iteration) {
			t.Errorf("settings saves = %d, want %d", settingsCalls.Load(), iteration)
		}
		cookie, _ := seedCookie.Load().(string)
		if cookie == "" || seedPriceCalls.Load() == 0 {
			t.Fatal("seed did not import prices with its authenticated session")
		}
		assertSeedTestCleanup(t, runtime, cookie)
		var channels, versions int
		if err := runtime.db.QueryRow(`SELECT COUNT(*) FROM model_prices WHERE price_scope = 'channel'`).Scan(&channels); err != nil {
			t.Fatal(err)
		}
		if err := runtime.db.QueryRow(`SELECT COUNT(*) FROM model_price_versions WHERE price_id IS NOT NULL`).Scan(&versions); err != nil {
			t.Fatal(err)
		}
		if channels != 1 || versions != 1 {
			t.Fatalf("seeded channel prices/versions = %d/%d, want 1/1", channels, versions)
		}
		now := time.Now().UTC().Format("2006-01-02T15:04:05.999999-07:00")
		result, err := runtime.db.Exec(`INSERT INTO usage_records
			(created_at, timestamp, provider, model, input_tokens, output_tokens, total_tokens, dedupe_key, raw_json)
			VALUES (?, ?, 'openai-compatible-deepseek', 'deepseek-v4-flash', 1000, 1000, 2000, ?, '{}')`, now, now, fmt.Sprintf("seed-identity-%d", iteration))
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		var detail struct {
			Unpriced  bool    `json:"unpriced"`
			Cost      float64 `json:"estimated_cost_usd"`
			Breakdown struct {
				Reason string `json:"unpriced_reason"`
			} `json:"cost_breakdown"`
		}
		if err := runtime.admin.do(http.MethodGet, fmt.Sprintf("/api/usage/records/%d?scope=admin", id), nil, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Unpriced || math.Abs(detail.Cost-0.00042) > 1e-10 {
			t.Errorf("usage after seed without restart: unpriced=%t, cost=%g, reason=%q; want priced at 0.00042", detail.Unpriced, detail.Cost, detail.Breakdown.Reason)
		}
	}
}

func TestAcceptanceSeedCleansUserWhenDemotionFails(t *testing.T) {
	var seedCookie atomic.Value
	runtime := newSeedTestRuntime(t, func(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/settings" {
			seedCookie.Store(r.Header.Get("Cookie"))
			if err := seedTestRelatedKey(db); err != nil {
				t.Errorf("create related seed key: %v", err)
			}
		}
		if r.URL.Path == "/api/model-prices" {
			t.Error("seed imported prices after demotion failed")
		}
		return false
	})
	if _, err := runtime.db.Exec(`CREATE TRIGGER fail_seed_demotion BEFORE UPDATE OF is_super_admin ON users
		WHEN OLD.username = 'seed-bot' BEGIN SELECT RAISE(ABORT, 'injected demotion failure'); END`); err != nil {
		t.Fatal(err)
	}
	cpa := newSeedTestCPA(t)
	output, err := runSeedTestCommand(t, runtime, cpa.URL)
	if err == nil || !strings.Contains(output, "drop seed super-admin role") || !strings.Contains(output, "injected demotion failure") {
		t.Errorf("seed error = %v, output = %s; want explicit demotion failure", err, output)
	}
	cookie, _ := seedCookie.Load().(string)
	assertSeedTestCleanup(t, runtime, cookie)
}

func TestAcceptanceSeedReportsCleanupFailure(t *testing.T) {
	for _, failLogin := range []bool{true, false} {
		t.Run(fmt.Sprintf("login_failure_%t", failLogin), func(t *testing.T) {
			runtime := newSeedTestRuntime(t, func(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api/auth/login" {
					return false
				}
				if _, err := db.Exec(`CREATE TRIGGER fail_seed_cleanup BEFORE DELETE ON users
					WHEN OLD.username = 'seed-bot' BEGIN SELECT RAISE(ABORT, 'injected cleanup failure'); END`); err != nil {
					t.Errorf("inject cleanup failure: %v", err)
				}
				if failLogin {
					http.Error(w, "injected login failure", http.StatusInternalServerError)
					return true
				}
				return false
			})
			cpa := newSeedTestCPA(t)
			output, err := runSeedTestCommand(t, runtime, cpa.URL)
			if err == nil || !strings.Contains(output, "remove seed user") || !strings.Contains(output, "injected cleanup failure") {
				t.Errorf("seed error = %v, output = %s; want cleanup error and nonzero exit", err, output)
			}
			if failLogin && !strings.Contains(output, "injected login failure") {
				t.Errorf("cleanup error replaced the original login error: %s", output)
			}
			if strings.Contains(output, "seed user removed") {
				t.Errorf("seed falsely reported cleanup success: %s", output)
			}
			if _, err := runtime.db.Exec(`DROP TRIGGER fail_seed_cleanup`); err != nil {
				t.Fatal(err)
			}
			if output, err := runSeedTestCommand(t, runtime, "unused-in-cleanup-mode", "-cleanup-only"); err != nil {
				t.Fatalf("cleanup-only after removing injected failure: %v: %s", err, output)
			}
			assertSeedTestCleanup(t, runtime, "")
		})
	}
}

type seedTestRoundTripper func(*http.Request) (*http.Response, error)

func (fn seedTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func TestSeedClientReportsResponseReadFailure(t *testing.T) {
	readErr := errors.New("injected response read failure")
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			api := client{base: "http://127.0.0.1", http: &http.Client{Transport: seedTestRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(iotest.ErrReader(readErr))}, nil
			})}}
			err := api.do(http.MethodPost, "/api/settings", map[string]any{}, nil)
			if !errors.Is(err, readErr) || !strings.Contains(err.Error(), "read response") || !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Errorf("response read error = %v, want original read error with request/status context", err)
			}
		})
	}
}
