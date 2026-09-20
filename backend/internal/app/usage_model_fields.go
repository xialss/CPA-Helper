package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
)

func usageResponseModelFields(raw string) (*string, *string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return nil, nil
	}
	read := func(key string) *string {
		var value string
		if json.Unmarshal(fields[key], &value) != nil {
			return nil
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		return &value
	}
	return read("response_model"), read("alias")
}

// Bind to the batch's management origin, never credentials or later configuration.
func usageCollectorOrigin(cfg CollectorConfig) string {
	base, err := collectorManagementHTTPURL(cfg.CLIProxyURL)
	if err != nil {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	sum := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(sum[:])
}
