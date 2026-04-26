package cost

// Usage groups the four token counters most providers expose. CachedRead
// and CacheWrite are optional; a zero value is treated as "no cache
// activity" (they are NOT silently re-billed at full input rate).
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CachedReadTokens int64
	CacheWriteTokens int64
}

// Calculator turns a Usage bundle into a USD estimate using the given
// catalog. Construct one per gil process; mutating Catalog is unsafe while
// callers are running Estimate concurrently, but Estimate itself is
// read-only and goroutine-safe given a stable Catalog.
type Calculator struct {
	Catalog Catalog
}

// NewCalculator returns a Calculator backed by the embedded default
// catalog. Callers wanting to honour user overrides should call
// LoadCatalog and assign the result directly: c := &Calculator{Catalog: cat}.
func NewCalculator() *Calculator {
	return &Calculator{Catalog: DefaultCatalog()}
}

// Estimate computes the USD cost for u under the given model. found=false
// signals an unknown model; usd is 0 in that case so callers can show
// "unknown model" without a NaN/zero ambiguity.
//
// The cached-read and cache-write rates fall back to InputPerM when the
// catalog entry omits them — matching Anthropic's billing for providers
// that don't price caching separately.
func (c *Calculator) Estimate(model string, u Usage) (usd float64, found bool) {
	if c == nil || c.Catalog == nil {
		return 0, false
	}
	price, ok := c.Catalog[model]
	if !ok {
		return 0, false
	}
	cachedRate := price.CachedReadPerM
	if cachedRate == 0 {
		cachedRate = price.InputPerM
	}
	writeRate := price.CacheWritePerM
	if writeRate == 0 {
		writeRate = price.InputPerM
	}
	const perMillion = 1_000_000.0
	usd = float64(u.InputTokens)/perMillion*price.InputPerM +
		float64(u.OutputTokens)/perMillion*price.OutputPerM +
		float64(u.CachedReadTokens)/perMillion*cachedRate +
		float64(u.CacheWriteTokens)/perMillion*writeRate
	return usd, true
}
