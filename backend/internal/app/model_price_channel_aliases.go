package app

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"
)

type modelPriceChannelAlias struct {
	AuthType            string `json:"auth_type"`
	ChannelBrand        string `json:"channel_brand"`
	ChannelKey          string `json:"channel_key"`
	Label               string `json:"label"`
	ChannelIdentityHash string `json:"channel_identity_hash,omitempty"`
}

func (a *App) listModelPriceChannelAliases(ctx context.Context) ([]modelPriceChannelAlias, error) {
	// Compatible channels use the configured Provider name. Keep legacy aliases
	// stored for explicit clearing, but never use them for display.
	rows, err := a.db.QueryContext(ctx, `SELECT auth_type, channel_brand, channel_key, label FROM model_price_channel_aliases WHERE channel_brand <> ? ORDER BY channel_brand, channel_key`, string(aiProviderBrandOpenAICompatibility))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]modelPriceChannelAlias, 0)
	for rows.Next() {
		var item modelPriceChannelAlias
		if err := rows.Scan(&item.AuthType, &item.ChannelBrand, &item.ChannelKey, &item.Label); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func modelPriceChannelAliasKey(authType, brand, channelKey string) modelPriceChannelGroupIdentity {
	return modelPriceChannelGroupIdentityKey(authType, aiProviderBrand(strings.TrimSpace(brand)), channelKey)
}

func modelPriceChannelAliasSelector(provider aiProviderItem) string {
	key, _, _ := modelPriceChannelSelector(provider)
	// OpenAI-compatible providers keep their canonical upstream name as the
	// stable selector, including when clearing a legacy local alias.
	if key == "" && provider.Brand != aiProviderBrandOpenAICompatibility {
		if hash := strings.TrimSpace(aiProviderOptionalString(provider.APIKeyHash)); hash != "" {
			return "keyhash:" + hash
		}
	}
	return key
}

func (a *App) upsertModelPriceChannelAlias(ctx context.Context, payload modelPriceChannelAlias) (modelPriceChannelAlias, error) {
	payload.AuthType = normalizeModelPriceChannelAuthType(payload.AuthType)
	payload.ChannelBrand = strings.ToLower(strings.TrimSpace(payload.ChannelBrand))
	payload.ChannelKey = canonicalModelPriceChannelKey(payload.ChannelBrand, payload.ChannelKey)
	payload.ChannelIdentityHash = strings.TrimSpace(payload.ChannelIdentityHash)
	payload.Label = strings.TrimSpace(payload.Label)
	if payload.AuthType != modelPriceChannelAuthTypeAPIKey || !isModelPriceChannelBrand(payload.ChannelBrand) || payload.ChannelKey == "" || len(payload.ChannelKey) > 500 || strings.ContainsRune(payload.ChannelKey, '\x00') {
		return payload, validationError("渠道身份不完整")
	}
	for _, r := range payload.Label {
		if unicode.IsControl(r) {
			return payload, validationError("渠道名称不能包含控制字符或换行")
		}
	}
	if len([]rune(payload.Label)) > 200 {
		return payload, validationError("渠道名称不能超过 200 个字符")
	}
	// Clearing a local display alias must remain possible after its upstream
	// channel has been removed. The alias identity itself is sufficient for a
	// scoped delete and never changes billing selectors or upstream settings.
	if payload.Label == "" {
		if _, err := a.db.ExecContext(ctx, `DELETE FROM model_price_channel_aliases WHERE auth_type = ? AND channel_brand = ? AND channel_key = ?`, payload.AuthType, payload.ChannelBrand, payload.ChannelKey); err != nil {
			return payload, err
		}
		return payload, nil
	}
	if payload.ChannelBrand == string(aiProviderBrandOpenAICompatibility) {
		return payload, validationError("OpenAI-compatible 渠道名称请在渠道编辑页修改 Provider 名称")
	}
	// Names are display metadata; resolve the current credential without changing
	// its upstream config or invalidating the billing selector snapshot.
	providers, err := a.aiProviderConfigSnapshot(ctx)
	if err != nil {
		return payload, err
	}
	matches := 0
	for _, provider := range providers {
		key := modelPriceChannelAliasSelector(provider)
		if string(provider.Brand) != payload.ChannelBrand || key != payload.ChannelKey {
			continue
		}
		matches++
		if payload.ChannelIdentityHash == "" || provider.IdentityHash != payload.ChannelIdentityHash {
			return payload, conflictError("渠道配置已变化，请刷新后重试")
		}
	}
	if matches != 1 {
		return payload, conflictError("渠道配置已变化，请刷新后重试")
	}
	now := dbTime(time.Now())
	_, err = a.db.ExecContext(ctx, `
		INSERT INTO model_price_channel_aliases (auth_type, channel_brand, channel_key, label, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(auth_type, channel_brand, channel_key) DO UPDATE SET label = excluded.label, updated_at = excluded.updated_at
	`, payload.AuthType, payload.ChannelBrand, payload.ChannelKey, payload.Label, now, now)
	return payload, err
}

func (a *App) modelPriceChannelAliasLabels(ctx context.Context) (map[modelPriceChannelGroupIdentity]string, error) {
	aliases, err := a.listModelPriceChannelAliases(ctx)
	if err != nil {
		return nil, err
	}
	labels := make(map[modelPriceChannelGroupIdentity]string, len(aliases))
	for _, alias := range aliases {
		labels[modelPriceChannelAliasKey(alias.AuthType, alias.ChannelBrand, alias.ChannelKey)] = alias.Label
	}
	return labels, nil
}

func (a *App) applyModelPriceChannelAliases(ctx context.Context, providers []aiProviderItem) error {
	labels, err := a.modelPriceChannelAliasLabels(ctx)
	if err != nil {
		return err
	}
	for i := range providers {
		provider := &providers[i]
		key := modelPriceChannelAliasSelector(*provider)
		provider.ChannelKey = key
		provider.ChannelAlias = labels[modelPriceChannelAliasKey(modelPriceChannelAuthTypeAPIKey, string(provider.Brand), key)]
	}
	return nil
}

func (a *App) handleModelPriceChannelAliases(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		aliases, err := a.listModelPriceChannelAliases(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, aliases)
		return nil
	case http.MethodPut:
		var payload modelPriceChannelAlias
		if err := decodeJSON(r, &payload); err != nil {
			return err
		}
		alias, err := a.upsertModelPriceChannelAlias(r.Context(), payload)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, alias)
		return nil
	default:
		return methodNotAllowed()
	}
}
