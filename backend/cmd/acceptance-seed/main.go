package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cpa-helper/backend/internal/security"

	_ "modernc.org/sqlite"
)

// Acceptance seed tool (see .trellis/spec/guides/acceptance-seed-data.md):
// points an isolated CPA-Helper instance at cmd/acceptance-fake-cpa, then
// creates library and channel prices through the public API using a throwaway
// admin account that is removed afterwards. Never run against a real data dir.

const (
	seedUsername                  = "seed-bot"
	acceptanceIsolationMarkerName = ".cpa-helper-acceptance"
	acceptanceIsolationMarker     = "CPA-Helper isolated acceptance runtime v1"
)

type rates struct {
	Input, Output, CacheRead, CacheWrite float64
	LongContextThreshold                 int64
	LongContextMultiplier                float64
}

var libraryPrices = map[string]map[string]rates{
	"anthropic": {
		"claude-opus-5":             {Input: 15, Output: 75, CacheRead: 1.5, CacheWrite: 18.75, LongContextThreshold: 200000, LongContextMultiplier: 2},
		"claude-sonnet-5":           {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, LongContextThreshold: 200000, LongContextMultiplier: 2},
		"claude-haiku-4-5-20251001": {Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25},
	},
	"openai": {
		"gpt-5.6-terra": {Input: 1.25, Output: 10, CacheRead: 0.125},
		"gpt-5.6-sol":   {Input: 0.25, Output: 2, CacheRead: 0.025},
		"gpt-6-astra":   {Input: 5, Output: 40, CacheRead: 0.5},
	},
	"deepseek": {
		"deepseek-v4-pro":   {Input: 0.56, Output: 1.68, CacheRead: 0.07, LongContextThreshold: 128000, LongContextMultiplier: 1.5},
		"deepseek-v4-flash": {Input: 0.14, Output: 0.28, CacheRead: 0.014},
		"deepseek-reasoner": {Input: 0.55, Output: 2.19, CacheRead: 0.14},
	},
}

// Channel price differs per channel so the UI shows distinct numbers.
var channelMultipliers = map[string]float64{
	"claude-primary": 1, "claude-backup": 0.9, "claude-relay": 1.15,
	"codex-primary": 1, "codex-backup": 0.85, "codex-relay": 1.2,
	"deepseek": 1, "deepseek-silicon": 0.8, "deepseek-volc": 1.1,
}

func libraryProviderForBrand(brand, name string) string {
	switch brand {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	case "openai_compatibility":
		if strings.HasPrefix(name, "deepseek") {
			return "deepseek"
		}
	}
	return ""
}

type providerItem struct {
	Brand        string  `json:"brand"`
	IdentityHash string  `json:"identity_hash"`
	ChannelKey   string  `json:"channel_key"`
	Name         *string `json:"name"`
	Models       []struct {
		Name string `json:"name"`
	} `json:"models"`
}

type client struct {
	base string
	http *http.Client
}

func (c *client) do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("%s %s -> %d: read response: %w", method, path, response.StatusCode, err)
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("%s %s -> %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out != nil {
		return json.Unmarshal(payload, out)
	}
	return nil
}

func pricePayload(provider, model string, r rates, multiplier float64) map[string]any {
	payload := map[string]any{
		"provider":                       provider,
		"model":                          model,
		"billing_unit":                   "token",
		"input_usd_per_million":          round(r.Input * multiplier),
		"output_usd_per_million":         round(r.Output * multiplier),
		"cache_read_usd_per_million":     round(r.CacheRead * multiplier),
		"cache_creation_usd_per_million": round(r.CacheWrite * multiplier),
	}
	if r.LongContextThreshold > 0 {
		payload["long_context"] = map[string]any{
			"threshold_input_tokens":         r.LongContextThreshold,
			"input_usd_per_million":          round(r.Input * multiplier * r.LongContextMultiplier),
			"output_usd_per_million":         round(r.Output * multiplier * r.LongContextMultiplier),
			"cache_read_usd_per_million":     round(r.CacheRead * multiplier * r.LongContextMultiplier),
			"cache_creation_usd_per_million": round(r.CacheWrite * multiplier * r.LongContextMultiplier),
		}
	}
	return payload
}

func round(v float64) float64 {
	return float64(int64(v*1e6+0.5)) / 1e6
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func removeSeedUser(db *sql.DB) error {
	var userID int64
	err := db.QueryRow(`SELECT id FROM users WHERE username = ?`, seedUsername).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	tables.Close()
	for _, table := range names {
		fks, err := db.Query(fmt.Sprintf(`PRAGMA foreign_key_list("%s")`, table))
		if err != nil {
			return err
		}
		var columns []string
		for fks.Next() {
			var id, seq int
			var refTable, from, to string
			var onUpdate, onDelete, match sql.NullString
			if err := fks.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				return err
			}
			if refTable == "users" {
				columns = append(columns, from)
			}
		}
		fks.Close()
		for _, column := range columns {
			if _, err := db.Exec(fmt.Sprintf(`DELETE FROM "%s" WHERE "%s" = ?`, table, column), userID); err != nil {
				return fmt.Errorf("clean %s.%s: %w", table, column, err)
			}
		}
	}
	_, err = db.Exec(`DELETE FROM users WHERE id = ?`, userID)
	return err
}

func validateAcceptanceIsolation(acceptanceDir, dbPath string) error {
	if strings.TrimSpace(acceptanceDir) == "" {
		return fmt.Errorf("-acceptance-dir is required and must contain %s", acceptanceIsolationMarkerName)
	}
	root, err := filepath.Abs(acceptanceDir)
	if err != nil {
		return fmt.Errorf("resolve acceptance directory: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve acceptance directory symlinks: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("stat acceptance directory: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("acceptance directory is not a directory: %s", root)
	}

	marker, err := os.ReadFile(filepath.Join(root, acceptanceIsolationMarkerName))
	if err != nil {
		return fmt.Errorf("read acceptance isolation marker: %w", err)
	}
	if strings.TrimSpace(string(marker)) != acceptanceIsolationMarker {
		return fmt.Errorf("invalid acceptance isolation marker in %s", root)
	}

	expectedDBPath := filepath.Join(root, "data", "db", "cpa_helper.sqlite3")
	resolvedExpectedDBPath, err := filepath.EvalSymlinks(expectedDBPath)
	if err != nil {
		return fmt.Errorf("resolve isolated database path: %w", err)
	}
	if err := requirePathWithin(resolvedExpectedDBPath, root); err != nil {
		return fmt.Errorf("isolated database path escapes acceptance directory: %w", err)
	}
	resolvedDBPath, err := filepath.Abs(dbPath)
	if err != nil {
		return fmt.Errorf("resolve supplied database path: %w", err)
	}
	resolvedDBPath, err = filepath.EvalSymlinks(resolvedDBPath)
	if err != nil {
		return fmt.Errorf("resolve supplied database path: %w", err)
	}
	if err := requirePathWithin(resolvedDBPath, root); err != nil {
		return fmt.Errorf("supplied database path escapes acceptance directory: %w", err)
	}
	expectedInfo, err := os.Stat(resolvedExpectedDBPath)
	if err != nil {
		return fmt.Errorf("stat isolated database: %w", err)
	}
	suppliedInfo, err := os.Stat(resolvedDBPath)
	if err != nil {
		return fmt.Errorf("stat supplied database: %w", err)
	}
	if !os.SameFile(expectedInfo, suppliedInfo) {
		return fmt.Errorf("-db must be the isolated database %s", expectedDBPath)
	}
	return nil
}

func requirePathWithin(path, root string) error {
	// Both paths are absolute and already resolved; keep the marked root fixed.
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("%s is outside %s", path, root)
	}
	return nil
}

func validateLoopbackURL(name, rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute loopback URL, got %q", name, rawURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https, got %q", name, rawURL)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not include credentials", name)
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must target loopback only, got %q", name, rawURL)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (runErr error) {
	dbPath := flag.String("db", "", "path to cpa_helper.sqlite3")
	acceptanceDir := flag.String("acceptance-dir", "", "isolated acceptance root containing .cpa-helper-acceptance and data/db/cpa_helper.sqlite3")
	backend := flag.String("backend", "http://127.0.0.1:18318", "CPA-Helper backend base URL")
	cpa := flag.String("cpa", "http://127.0.0.1:18319", "fake CPA management URL")
	cleanupOnly := flag.Bool("cleanup-only", false, "only remove the seed user")
	flag.Parse()
	if *dbPath == "" {
		return fmt.Errorf("-db is required")
	}
	if err := validateAcceptanceIsolation(*acceptanceDir, *dbPath); err != nil {
		return fmt.Errorf("refusing to mutate database outside an explicitly isolated acceptance runtime: %w", err)
	}
	if !*cleanupOnly {
		if err := validateLoopbackURL("-backend", *backend); err != nil {
			return err
		}
		if err := validateLoopbackURL("-cpa", *cpa); err != nil {
			return err
		}
	}
	db, err := sql.Open("sqlite", *dbPath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open isolated database: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close isolated database: %w", err))
		}
	}()
	if *cleanupOnly {
		if err := removeSeedUser(db); err != nil {
			return fmt.Errorf("remove seed user: %w", err)
		}
		fmt.Println("seed user removed")
		return nil
	}

	password, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("generate seed password: %w", err)
	}
	salt, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("generate seed password salt: %w", err)
	}
	if err := removeSeedUser(db); err != nil {
		return fmt.Errorf("remove previous seed user: %w", err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.999999-07:00")
	// Settings are super-admin-only. Drop this temporary role after the normal
	// settings API has updated both persistence and the running selector cache.
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, password_salt, is_admin, is_super_admin, nickname, must_change_password, created_at, updated_at)
		VALUES (?, ?, ?, 1, 1, 'Seed Bot', 0, ?, ?)`, seedUsername, security.HashPassword(password, salt), salt, now, now); err != nil {
		return fmt.Errorf("create seed user: %w", err)
	}
	defer func() {
		if err := removeSeedUser(db); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("remove seed user: %w", err))
			return
		}
		fmt.Println("seed user removed")
	}()

	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("create seed cookie jar: %w", err)
	}
	api := &client{base: *backend, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	if err := api.do(http.MethodPost, "/api/auth/login", map[string]any{"username": seedUsername, "password": password}, nil); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if err := api.do(http.MethodPut, "/api/settings", map[string]any{"cliaproxy_url": *cpa, "management_key": "fake-management-key"}, nil); err != nil {
		return fmt.Errorf("save acceptance settings: %w", err)
	}
	result, err := db.Exec(`UPDATE users SET is_super_admin = 0, updated_at = ?
		WHERE username = ? AND is_admin = 1 AND is_super_admin = 1`, time.Now().UTC().Format("2006-01-02T15:04:05.999999-07:00"), seedUsername)
	if err != nil {
		return fmt.Errorf("drop seed super-admin role: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check seed super-admin role removal: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("drop seed super-admin role: updated %d users, want 1", affected)
	}

	created := 0
	for provider, modelsByName := range libraryPrices {
		for model, r := range modelsByName {
			payload := pricePayload(provider, model, r, 1)
			payload["price_scope"] = "library"
			if err := api.do(http.MethodPost, "/api/model-prices", payload, nil); err != nil {
				if strings.Contains(err.Error(), "已存在") {
					fmt.Printf("skip existing library price %s/%s\n", provider, model)
					continue
				}
				return fmt.Errorf("create library price %s/%s: %w", provider, model, err)
			}
			created++
		}
	}
	fmt.Printf("library prices created: %d\n", created)

	var snapshot struct {
		Providers []json.RawMessage `json:"providers"`
	}
	if err := api.do(http.MethodGet, "/api/ai-providers", nil, &snapshot); err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	created = 0
	for _, raw := range snapshot.Providers {
		var item providerItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("decode provider: %w", err)
		}
		name := ""
		if item.Name != nil {
			name = *item.Name
		}
		libraryProvider := libraryProviderForBrand(item.Brand, name)
		if libraryProvider == "" {
			fmt.Printf("skip provider brand=%s name=%q\n", item.Brand, name)
			continue
		}
		multiplier, ok := channelMultipliers[item.ChannelKey]
		if !ok {
			multiplier = 1
		}
		providerField := item.Brand
		if item.Brand == "openai_compatibility" {
			providerField = name
		}
		for _, model := range item.Models {
			r, ok := libraryPrices[libraryProvider][model.Name]
			if !ok {
				fmt.Printf("skip unknown model %s on %s\n", model.Name, item.ChannelKey)
				continue
			}
			payload := pricePayload(providerField, model.Name, r, multiplier)
			payload["price_scope"] = "channel"
			payload["channel_auth_type"] = "apikey"
			payload["channel_brand"] = item.Brand
			payload["channel_key"] = item.ChannelKey
			payload["channel_identity_hash"] = item.IdentityHash
			if err := api.do(http.MethodPost, "/api/model-prices", payload, nil); err != nil {
				if strings.Contains(err.Error(), "已存在") {
					fmt.Printf("skip existing channel price %s/%s\n", item.ChannelKey, model.Name)
					continue
				}
				return fmt.Errorf("create channel price %s/%s: %w", item.ChannelKey, model.Name, err)
			}
			created++
		}
	}
	fmt.Printf("channel prices created: %d\n", created)
	return nil
}
