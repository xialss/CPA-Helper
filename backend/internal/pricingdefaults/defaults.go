package pricingdefaults

import "strings"

// LongContextPrice describes the complete request-wide price band used after a
// model-specific input-token threshold is exceeded.
type LongContextPrice struct {
	ThresholdInputTokens       int64
	InputUSDPerMillion         float64
	OutputUSDPerMillion        float64
	CacheReadUSDPerMillion     float64
	CacheCreationUSDPerMillion float64
}

type ModelLongContextPrice struct {
	Provider string
	Model    string
	Price    LongContextPrice
}

var modelLongContextPrices = []ModelLongContextPrice{
	{
		Provider: "openai",
		Model:    "gpt-5.6-terra",
		Price: LongContextPrice{
			ThresholdInputTokens:       272_000,
			InputUSDPerMillion:         5,
			OutputUSDPerMillion:        22.5,
			CacheReadUSDPerMillion:     0.5,
			CacheCreationUSDPerMillion: 6.25,
		},
	},
	{
		Provider: "gemini",
		Model:    "gemini-2.5-pro",
		Price: LongContextPrice{
			ThresholdInputTokens:       200_000,
			InputUSDPerMillion:         2.5,
			OutputUSDPerMillion:        15,
			CacheReadUSDPerMillion:     0.25,
			CacheCreationUSDPerMillion: 0,
		},
	},
	{
		Provider: "gemini",
		Model:    "gemini-3.1-pro-preview",
		Price: LongContextPrice{
			ThresholdInputTokens:       200_000,
			InputUSDPerMillion:         4,
			OutputUSDPerMillion:        18,
			CacheReadUSDPerMillion:     0.4,
			CacheCreationUSDPerMillion: 0,
		},
	},
}

func LookupLongContext(provider, model string) (LongContextPrice, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	for _, item := range modelLongContextPrices {
		if item.Provider == provider && item.Model == model {
			return item.Price, true
		}
	}
	return LongContextPrice{}, false
}

func AllLongContext() []ModelLongContextPrice {
	result := make([]ModelLongContextPrice, len(modelLongContextPrices))
	copy(result, modelLongContextPrices)
	return result
}
