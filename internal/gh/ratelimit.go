package gh

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateSnapshot is the primary REST rate-limit budget as reported by the most
// recent GitHub response. Known is false until a response has carried the
// headers, which is how callers tell "no data yet" from a genuine zero.
type RateSnapshot struct {
	Limit     int
	Remaining int
	Used      int
	Reset     time.Time
	Known     bool
}

// GitHub's rate-limit response headers. Restated here rather than reused from
// go-github, which keeps them unexported.
const (
	headerRateLimit     = "X-Ratelimit-Limit"
	headerRateRemaining = "X-Ratelimit-Remaining"
	headerRateUsed      = "X-Ratelimit-Used"
	headerRateReset     = "X-Ratelimit-Reset"
	headerRateResource  = "X-Ratelimit-Resource"
)

// rateObserver records the rate-limit headers of every response passing
// through it. It sits in the transport chain rather than at the call sites so
// that any REST call added later is accounted for without anyone remembering
// to instrument it — the same reasoning that centralises wrapAPIError.
//
// Only the "core" resource is recorded. Search and GraphQL carry their own,
// much smaller budgets under the same header names (search is 30/minute), so
// mixing them in would make the number meaningless: one search response would
// leave the dashboard reading "28/30".
type rateObserver struct {
	base http.RoundTripper

	mu   sync.Mutex
	snap RateSnapshot
}

// RoundTrip passes the request through and records the budget from the
// response headers. It never fails the request over a malformed header: a
// missing or unparsable value simply leaves the snapshot untouched.
func (o *rateObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := o.base.RoundTrip(req)
	if resp != nil {
		o.record(resp.Header)
	}
	return resp, err
}

func (o *rateObserver) record(h http.Header) {
	// An absent resource header means an endpoint that predates it; treat it as
	// core rather than dropping the reading.
	if res := h.Get(headerRateResource); res != "" && res != "core" {
		return
	}
	limit, errLimit := strconv.Atoi(h.Get(headerRateLimit))
	remaining, errRemaining := strconv.Atoi(h.Get(headerRateRemaining))
	if errLimit != nil || errRemaining != nil {
		return
	}
	snap := RateSnapshot{Limit: limit, Remaining: remaining, Known: true}
	if used, err := strconv.Atoi(h.Get(headerRateUsed)); err == nil {
		snap.Used = used
	}
	if reset, err := strconv.ParseInt(h.Get(headerRateReset), 10, 64); err == nil && reset != 0 {
		snap.Reset = time.Unix(reset, 0)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	o.snap = snap
}

func (o *rateObserver) snapshot() RateSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snap
}

// RateSnapshot returns the primary REST budget as of the client's most recent
// GitHub response. It costs nothing: the numbers ride on responses the scan
// already made, so no extra call is issued.
func (c *Client) RateSnapshot() RateSnapshot {
	return c.rates.snapshot()
}
