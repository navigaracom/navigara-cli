package telemetry

import "strings"

// claudePrice is USD per million tokens. Cache writes cost more for the 1-hour TTL.
type claudePrice struct {
	input, output, cacheRead, cacheWrite5m, cacheWrite1h float64
}

// claudePrices were fitted exactly against Claude Code's own api_request cost_usd. Longest prefix wins.
var claudePrices = map[string]claudePrice{
	"claude-fable-5-1":  {10, 50, 0.25, 12.5, 20},
	"claude-fable-5":    {10, 50, 1, 12.5, 20},
	"claude-opus-5-5":   {4, 20, 0.2, 5, 8},
	"claude-opus-5":     {5, 25, 0.5, 6.25, 10},
	"claude-opus-4":     {5, 25, 0.5, 6.25, 10},
	"claude-sonnet-5-5": {2, 10, 0.2, 2.5, 4},
	"claude-sonnet-5":   {3, 15, 0.3, 3.75, 6},
	"claude-sonnet-4":   {3, 15, 0.3, 3.75, 6},
	"claude-haiku-4":    {1, 5, 0.1, 1.25, 2},
}

func priceFor(model string) (claudePrice, bool) {
	model = strings.TrimSuffix(model, "[1m]")
	best, found := "", false
	for prefix := range claudePrices {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best, found = prefix, true
		}
	}
	return claudePrices[best], found
}

// claudeUsage is one API response's final token counts.
type claudeUsage struct {
	input, output, cacheRead, cacheWrite5m, cacheWrite1h int64
}

func (u claudeUsage) cacheCreation() int64 { return u.cacheWrite5m + u.cacheWrite1h }

func (u claudeUsage) cost(model string) (float64, bool) {
	p, ok := priceFor(model)
	if !ok {
		return 0, false
	}
	return (float64(u.input)*p.input + float64(u.output)*p.output + float64(u.cacheRead)*p.cacheRead +
		float64(u.cacheWrite5m)*p.cacheWrite5m + float64(u.cacheWrite1h)*p.cacheWrite1h) / 1e6, true
}
