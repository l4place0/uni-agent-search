package app

import (
	"errors"
	"time"
)

// providerOutcome is the common policy used by scheduled search and manual
// tests. Adapters report typed upstream errors; they never mutate Key state.
// Rotation is a SearchService decision; manual tests release once, never retry.
type providerOutcome struct {
	status   int
	cooldown int64
	retry    bool
	reason   string
}

func classifyProviderOutcome(err error, now time.Time) providerOutcome {
	if err == nil {
		return providerOutcome{status: 200}
	}
	out := providerOutcome{reason: err.Error()}
	var coded providerCodeError
	var upstream upstreamError
	switch {
	// Providers that answer HTTP 200 with a business code in the body.
	case errors.As(err, &coded):
		out.status = coded.status
		switch coded.status {
		case 401, 403:
			out.retry = true
		case 402, 429:
			out.cooldown = coded.cooldown
			if out.cooldown == 0 {
				out.cooldown = now.Add(time.Minute).Unix()
			}
			out.retry = true
		}
	case errors.As(err, &upstream):
		out.status = upstream.status
		switch upstream.status {
		case 401, 403:
			out.retry = true
		case 402, 429, 432, 433:
			out.cooldown = upstream.retry
			out.retry = true
		}
	}
	if out.status == 0 || out.status >= 500 {
		out.cooldown = now.Add(10 * time.Second).Unix()
	}
	return out
}
