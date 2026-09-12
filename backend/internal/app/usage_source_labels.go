package app

import (
	"context"
	"strings"
)

// This provider-level snapshot is display metadata only. In particular, a
// keyhash alias selector must never enter the billing selector index.
type usageSourceChannel struct {
	AuthIndex  string
	APIKeyHash string
	Count      int
}

type usageSourceChannelIndex map[modelPriceChannelGroupIdentity]usageSourceChannel

func usageSourceChannels(providers []aiProviderItem) usageSourceChannelIndex {
	channels := make(usageSourceChannelIndex, len(providers))
	for _, provider := range providers {
		key := modelPriceChannelAliasSelector(provider)
		if key == "" && provider.Brand == aiProviderBrandOpenAICompatibility {
			continue
		}
		identity := modelPriceChannelAliasKey(modelPriceChannelAuthTypeAPIKey, string(provider.Brand), key)
		channel := channels[identity]
		if channel.Count == 0 && provider.Brand != aiProviderBrandOpenAICompatibility {
			channel.AuthIndex = strings.TrimSpace(aiProviderOptionalString(provider.AuthIndex))
			channel.APIKeyHash = strings.TrimSpace(aiProviderOptionalString(provider.APIKeyHash))
		}
		channel.Count++
		channels[identity] = channel
	}
	return channels
}

func cloneUsageSourceChannels(source usageSourceChannelIndex) usageSourceChannelIndex {
	if source == nil {
		return nil
	}
	cloned := make(usageSourceChannelIndex, len(source))
	for identity, channel := range source {
		cloned[identity] = channel
	}
	return cloned
}

type usageSourceLabelContext struct {
	aliases        map[modelPriceChannelGroupIdentity]string
	channels       usageSourceChannelIndex
	labels         modelPriceChannelLabelIndex
	modelSelectors modelPriceChannelSelectorIndex
	selectors      modelPriceChannelSelectorIndex
}

func (a *App) usageSourceLabels(ctx context.Context, matchContext modelPriceMatchContext) (*usageSourceLabelContext, error) {
	aliases, err := a.modelPriceChannelAliasLabels(ctx)
	if err != nil {
		return nil, err
	}
	if !matchContext.SelectorsAvailable || matchContext.SourceChannels == nil {
		return nil, nil
	}
	result := &usageSourceLabelContext{
		aliases: aliases, channels: matchContext.SourceChannels, labels: matchContext.ChannelLabels,
		modelSelectors: matchContext.Selectors, selectors: modelPriceChannelSelectorIndex{},
	}
	// Empty model keys reuse the existing runtime-provider collision rules at
	// provider scope when the current model has no configured candidates.
	for identity, channel := range result.channels {
		key := identity.ChannelKey
		if identity.Brand != aiProviderBrandOpenAICompatibility {
			key = channel.AuthIndex
		}
		result.selectors[modelPriceChannelIdentityKey(identity.Brand, key, "")] += channel.Count
	}
	for identity := range aliases {
		if identity.AuthType != modelPriceChannelAuthTypeAPIKey {
			continue
		}
		if _, current := result.channels[identity]; current {
			continue
		}
		if identity.Brand != aiProviderBrandOpenAICompatibility && strings.HasPrefix(identity.ChannelKey, "keyhash:") {
			continue
		}
		// Retained aliases are exact historical identities, never display-label
		// matches. Keep them in collision detection after a provider is removed.
		result.selectors[modelPriceChannelIdentityKey(identity.Brand, identity.ChannelKey, "")]++
	}
	return result, nil
}

func (labels *usageSourceLabelContext) labelFor(record UsageRecord) *string {
	if labels == nil {
		return nil
	}
	auth, conflict := usageSourceCatalogAuth(record)
	if conflict || !isAPIKeyAuth(auth) {
		return nil
	}
	provider := strings.TrimSpace(aiProviderOptionalString(record.Provider))
	if provider == "" {
		return nil
	}
	authIndex := usageAnalyticsRecordAuthIndex(record)
	if rawIndex, storedIndex := usageAnalyticsRawAuthIndex(record.RawJSON), usageNonBlankText(record.AuthIndex); rawIndex != nil && storedIndex != nil && *rawIndex != *storedIndex {
		return nil
	}
	if model := normalizeModelPriceChannelModel(aiProviderOptionalString(record.Model)); model != "" {
		if strings.TrimSpace(aiProviderOptionalString(authIndex)) == "" {
			compatibleCount, nativeCount := configuredMissingAuthModelPriceCandidateCounts(labels.modelSelectors, provider, model)
			if compatibleCount > 0 && nativeCount > 0 {
				return nil
			}
		}
		matched, count := configuredModelPriceCandidates(labels.modelSelectors, provider, model, authIndex)
		if count > 0 {
			if count != 1 {
				return nil
			}
			if matched.Brand == aiProviderBrandOpenAICompatibility && labels.hasNativeCredentialConflict(record, provider, authIndex) {
				return nil
			}
			return labels.channelLabel(record, matched)
		}
	}
	if strings.TrimSpace(aiProviderOptionalString(authIndex)) == "" {
		compatibleCount, nativeCount := configuredMissingAuthModelPriceCandidateCounts(labels.selectors, provider, "")
		if nativeCount > 0 {
			if compatibleCount > 0 {
				return nil
			}
			return labels.keyHashLabel(record, provider)
		}
	}
	matched, count := configuredModelPriceCandidates(labels.selectors, provider, "", authIndex)
	if count != 1 {
		return nil
	}
	return labels.channelLabel(record, matched)
}

func (labels *usageSourceLabelContext) hasNativeCredentialConflict(record UsageRecord, provider string, authIndexValue *string) bool {
	brands := matchingNativePriceBrands(provider)
	if len(brands) == 0 {
		return false
	}
	sourceHash := usageSourceAPIKeyHash(record)
	if sourceHash == "" {
		return false
	}
	authIndex := strings.TrimSpace(aiProviderOptionalString(authIndexValue))
	channelKey := authIndex
	if channelKey == "" {
		channelKey = "keyhash:" + sourceHash
	}
	// Model lists can change while native credentials remain valid. A unique
	// compatible model candidate cannot override that surviving identity.
	for _, brand := range brands {
		identity := modelPriceChannelAliasKey(modelPriceChannelAuthTypeAPIKey, string(brand), channelKey)
		channel := labels.channels[identity]
		// A duplicated exact identity retains only its first hash, so it cannot
		// disprove a credential conflict regardless of provider order.
		if channel.Count > 1 || (channel.Count == 1 && channel.AuthIndex == authIndex && channel.APIKeyHash == sourceHash) {
			return true
		}
	}
	return false
}

func (labels *usageSourceLabelContext) channelLabel(record UsageRecord, matched modelPriceChannelIdentity) *string {
	identity := modelPriceChannelAliasKey(modelPriceChannelAuthTypeAPIKey, string(matched.Brand), matched.ChannelKey)
	channel := labels.channels[identity]
	// Model evidence cannot make a duplicate alias identity unique or choose
	// whichever credential hash happened to be stored first in the snapshot.
	if channel.Count > 1 {
		return nil
	}
	if matched.Brand != aiProviderBrandOpenAICompatibility {
		if sourceHash := usageSourceAPIKeyHash(record); sourceHash != "" && channel.APIKeyHash != "" && sourceHash != channel.APIKeyHash {
			return nil
		}
	}
	if label := labels.aliases[identity]; label != "" {
		return &label
	}
	if matched.Brand == aiProviderBrandOpenAICompatibility {
		if display, ok := labels.labels[identity]; ok && !display.LabelFallback && display.Label != "" {
			return &display.Label
		}
	}
	return nil
}

func (labels *usageSourceLabelContext) keyHashLabel(record UsageRecord, provider string) *string {
	sourceHash := usageSourceAPIKeyHash(record)
	if sourceHash == "" {
		return nil
	}
	var matched modelPriceChannelGroupIdentity
	var matchedChannel usageSourceChannel
	count := 0
	for _, brand := range matchingNativePriceBrands(provider) {
		for identity, channel := range labels.channels {
			if identity.Brand == brand && channel.APIKeyHash == sourceHash {
				matched, matchedChannel = identity, channel
				count += channel.Count
			}
		}
	}
	if count != 1 || matchedChannel.AuthIndex != "" || matched.ChannelKey != "keyhash:"+sourceHash {
		return nil
	}
	if label := labels.aliases[matched]; label != "" {
		return &label
	}
	return nil
}

func usageSourceAPIKeyHash(record UsageRecord) string {
	source := usageAnalyticsRecordSource(record)
	if source == nil {
		return ""
	}
	value := strings.TrimSpace(*source)
	// A masked value is not credential evidence. It may still accompany an
	// independently unique auth_index, but cannot resolve a keyhash alias.
	if value == "" || strings.Contains(value, "...") || strings.ContainsAny(value, "*…•") {
		return ""
	}
	return hashAPIKey(value)
}
