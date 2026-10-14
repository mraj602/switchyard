package scan

import (
	"sort"
	"strings"
)

// Port of core/pricing.js: API-equivalent USD per million tokens, matched by model prefix,
// longest prefix first. Only a common yardstick; subscriptions are not billed in dollars.
type rateRow struct {
	prefix            string
	input, output, rd float64
}

var rates = func() []rateRow {
	r := []rateRow{
		{"claude-fable-5-1", 10, 50, 0.25},
		{"claude-mythos-5-1", 10, 50, 0.25},
		{"claude-fable-5", 10, 50, 1.00},
		{"claude-mythos-5", 10, 50, 1.00},
		{"claude-opus-5-5", 4, 20, 0.20},
		{"claude-opus-5", 5, 25, 0.50},
		{"claude-opus-4-8", 5, 25, 0.50},
		{"claude-opus-4-7", 5, 25, 0.50},
		{"claude-opus-4-6", 5, 25, 0.50},
		{"claude-opus-4-5", 5, 25, 0.50},
		{"claude-opus-4", 15, 75, 1.50},
		{"claude-sonnet-5-5", 2, 10, 0.20},
		{"claude-sonnet-5", 2, 10, 0.20},
		{"claude-sonnet-4", 3, 15, 0.30},
		{"claude-haiku-5-5", 0.10, 0.50, 0.01},
		{"claude-haiku-4-5", 1, 5, 0.10},
		{"claude-3-5-haiku", 0.80, 4, 0.08},
	}
	sort.SliceStable(r, func(i, j int) bool { return len(r[i].prefix) > len(r[j].prefix) })
	return r
}()

// Rate is USD per million tokens.
type Rate struct{ Input, Output, Read, Write5m, Write1h float64 }

// RateFor returns the rate for a model id, or false for unknown models.
func RateFor(model string) (Rate, bool) {
	m := strings.ToLower(model)
	for _, r := range rates {
		if strings.HasPrefix(m, r.prefix) {
			return Rate{r.input, r.output, r.rd, r.input * 1.25, r.input * 2}, true
		}
	}
	return Rate{}, false
}

// Tokens are the five counters of a row.
type Tokens struct{ Input, Output, Cache5m, Cache1h, CacheRead int64 }

// CostOf is the API-equivalent price in USD; ok is false for unknown models.
func CostOf(model string, t Tokens) (usd float64, ok bool) {
	r, found := RateFor(model)
	if !found {
		return 0, false
	}
	return (float64(t.Input)*r.Input + float64(t.Output)*r.Output + float64(t.Cache5m)*r.Write5m +
		float64(t.Cache1h)*r.Write1h + float64(t.CacheRead)*r.Read) / 1e6, true
}

var families = [][2]string{{"fable", "Fable"}, {"mythos", "Mythos"}, {"opus", "Opus"}, {"sonnet", "Sonnet"}, {"haiku", "Haiku"}}

// ModelLabel: "claude-opus-5-5" -> "Opus 5.5"; unknown ids are returned unchanged.
func ModelLabel(model string) string {
	for _, f := range families {
		i := strings.Index(model, f[0])
		if i < 0 {
			continue
		}
		rest := model[i+len(f[0]):]
		// JS: split(family)[1] is the text up to the next occurrence of the family.
		if j := strings.Index(rest, f[0]); j >= 0 {
			rest = rest[:j]
		}
		rest = strings.TrimPrefix(rest, "-")
		if len(rest) >= 9 && rest[len(rest)-9] == '-' && isNum(rest[len(rest)-8:]) {
			rest = rest[:len(rest)-9]
		}
		var ver []string
		for _, p := range strings.Split(rest, "-") {
			if isNum(p) {
				ver = append(ver, p)
			}
		}
		if len(ver) > 0 {
			return f[1] + " " + strings.Join(ver, ".")
		}
		return f[1]
	}
	if model == "" {
		return "unknown"
	}
	return model
}

func isNum(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
