package usage

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
)

// PricesFile is the member's own price file, in Brigade's config
// directory: the prices Brigade does not ship, or ones the member changes
// (Frank's decision of 2026-11-26, work item 349, ADR 0037).
const PricesFile = "prices.json"

// A Price is a model's list price, in US dollars for each million tokens,
// and where it came from.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	// Source is the page or the person the price came from, and AsOf the
	// day it was read there.
	Source string `json:"source,omitzero"`
	AsOf   string `json:"as_of,omitzero"`
}

// anthropicPricing is where the shipped prices were read, and when.
const (
	anthropicPricing = "https://platform.claude.com/docs/en/about-claude/pricing"
	anthropicAsOf    = "2026-11-26"
)

// claude is a Claude model's price as the pricing page gives it: input,
// output, cache hit, and the 5-minute cache write. Brigade counts cache
// writes as one number, so they are priced at the 5-minute rate.
func claude(input, output, cacheRead, cacheWrite float64) Price {
	return Price{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite, Source: anthropicPricing, AsOf: anthropicAsOf}
}

// shipped is the prices Brigade can cite, by model id. Claude Haiku 5.5
// costs more for a prompt over 100,000 tokens; a sum of tokens has lost
// each prompt's size, so its lower price stands for all.
var shipped = map[string]Price{
	"claude-fable-5-2":  claude(8, 40, 0.20, 10),
	"claude-fable-5-1":  claude(10, 50, 0.25, 12.50),
	"claude-fable-5":    claude(10, 50, 1, 12.50),
	"claude-opus-5-5":   claude(4, 20, 0.20, 5),
	"claude-opus-5":     claude(5, 25, 0.50, 6.25),
	"claude-opus-4-8":   claude(5, 25, 0.50, 6.25),
	"claude-sonnet-5-5": claude(2, 10, 0.10, 2.50),
	"claude-sonnet-5":   claude(2, 10, 0.20, 2.50),
	"claude-haiku-5-5":  claude(0.10, 0.50, 0.01, 0.125),
}

// Prices is the table a cost is estimated from: the shipped prices, with
// the member's file over them.
type Prices map[string]Price

// LoadPrices is the shipped prices with the member's file in configDir
// over them. A file that is not there is no error; one that cannot be
// read, or a price in it that is negative or not finite, is: the shipped
// prices are returned with it, so a cost is still estimated.
func LoadPrices(configDir string) (Prices, error) {
	p := Prices{}
	for k, v := range shipped {
		p[k] = v
	}
	if configDir == "" {
		return p, nil
	}
	data, err := readCapped(filepath.Join(configDir, PricesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	var doc struct {
		Models map[string]Price `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return p, errors.New("usage: the prices file is not JSON of the expected shape")
	}
	for k, v := range doc.Models {
		if !validPrice(v) {
			return p, errors.New("usage: a price in the prices file is negative or not a number")
		}
		if v.Source == "" {
			v.Source = PricesFile
		}
		p[modelKey(k)] = v
	}
	return p, nil
}

func validPrice(p Price) bool {
	for _, f := range []float64{p.Input, p.Output, p.CacheRead, p.CacheWrite} {
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
	}
	return true
}

// For is the price of a model, as a record names it: Claude Code's
// "claude-opus-5-5[1m]" is "claude-opus-5-5".
func (p Prices) For(model string) (Price, bool) {
	v, ok := p[modelKey(model)]
	return v, ok
}

// modelKey is a model id as the table keys it: lower case, without a
// bracketed suffix such as Claude Code's context size.
func modelKey(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(model, '['); i > 0 {
		model = model[:i]
	}
	return model
}

// Counts are plain token counts: what one stretch of a session spent.
type Counts struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// Zero reports whether nothing was spent.
func (c Counts) Zero() bool { return c == Counts{} }

// Add adds d to c, saturating.
func (c *Counts) Add(d Counts) {
	add := func(a, b int64) int64 {
		if s := a + b; s >= a {
			return s
		}
		return math.MaxInt64
	}
	c.Input, c.Output = add(c.Input, d.Input), add(c.Output, d.Output)
	c.CacheRead, c.CacheWrite = add(c.CacheRead, d.CacheRead), add(c.CacheWrite, d.CacheWrite)
}

// Cost is what c costs at that price, in US dollars.
func (p Price) Cost(c Counts) float64 {
	return (float64(c.Input)*p.Input + float64(c.Output)*p.Output +
		float64(c.CacheRead)*p.CacheRead + float64(c.CacheWrite)*p.CacheWrite) / 1e6
}

// CountsOf is the counts of a record's tokens; a count the harness does
// not give is zero here, as it adds nothing to a cost.
func CountsOf(t Tokens) Counts {
	return Counts{Input: t.Input.Or(0), Output: t.Output.Or(0), CacheRead: t.CacheRead.Or(0), CacheWrite: t.CacheWrite.Or(0)}
}

// Since is what was spent from prev to now: each count's growth. A count
// that fell means the harness started again (Claude Code's /clear); then
// the whole of now is new.
func Since(prev, now Counts) Counts {
	if now.Input < prev.Input || now.Output < prev.Output || now.CacheRead < prev.CacheRead || now.CacheWrite < prev.CacheWrite {
		return now
	}
	return Counts{Input: now.Input - prev.Input, Output: now.Output - prev.Output, CacheRead: now.CacheRead - prev.CacheRead, CacheWrite: now.CacheWrite - prev.CacheWrite}
}

// An ItemUsage is what one session spent on one work item: the tokens,
// and their estimated cost at list price. CostUSD sums the stretches whose
// model has a price; Unpriced counts the stretches whose model has none,
// and Model is the last model, which the page names when no price was
// found.
type ItemUsage struct {
	Number   int     `json:"number"`
	Tokens   Counts  `json:"tokens"`
	CostUSD  float64 `json:"cost_usd"`
	Priced   int64   `json:"priced"`
	Unpriced int64   `json:"unpriced"`
	Model    string  `json:"model,omitzero"`
}

// Add adds one stretch: its tokens, priced at the model's price when the
// table has one.
func (u *ItemUsage) Add(number int, model string, c Counts, prices Prices) {
	u.Number, u.Model = number, model
	u.Tokens.Add(c)
	if p, ok := prices.For(model); ok {
		u.CostUSD += p.Cost(c)
		u.Priced++
	} else {
		u.Unpriced++
	}
}
