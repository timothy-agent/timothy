package fxrates

// Convert turns amount in `from` into `to` using a USD-base rate table
// (quote currency -> units per 1 USD, as LatestUSDRates/FetchLatest
// return). Pure and I/O-free so it's trivially table-testable. Only a
// USD-base cross (from -> USD -> to) is ever computed — no transitive
// guessing beyond that one hop, per design. ok is false whenever a
// rate this conversion needs is missing; callers must never guess a
// rate themselves on that path.
func Convert(amount float64, from, to string, usdRates map[string]Rate) (result float64, asOf Rate, ok bool) {
	if from == to {
		return amount, Rate{}, true
	}

	// from -> USD: dividing by from's per-USD rate. USD itself has an
	// implicit rate of 1 and is never stored.
	var usdAmount float64
	var fromRate Rate
	switch from {
	case "USD":
		usdAmount = amount
	default:
		r, found := usdRates[from]
		if !found || r.Value <= 0 {
			return 0, Rate{}, false
		}
		fromRate = r
		usdAmount = amount / r.Value
	}

	if to == "USD" {
		return usdAmount, fromRate, true
	}

	toRate, found := usdRates[to]
	if !found || toRate.Value <= 0 {
		return 0, Rate{}, false
	}
	result = usdAmount * toRate.Value

	// asOf reports whichever leg's rate is older — the honest
	// provenance date for a cross computed from two independently
	// fetched (but same-source) day-rates. When only one leg used a
	// real stored rate (the other was the implicit USD=1), that leg's
	// date is the answer.
	asOf = toRate
	if from != "USD" && fromRate.AsOf.Before(toRate.AsOf) {
		asOf = fromRate
	}
	return result, asOf, true
}
