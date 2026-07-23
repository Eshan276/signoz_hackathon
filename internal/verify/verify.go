// Package verify confirms that telemetry actually reached SigNoz after
// instrumentation is applied.
//
// This is what separates a config generator from a tool you can trust: anything
// can write YAML, but reporting "3 services reporting, 47 spans" means the whole
// pipeline — injection, export, ingest, storage — demonstrably works.
package verify

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ServiceReport is what one service is sending.
type ServiceReport struct {
	Name      string
	SpanCount int
	// Operations seen, useful for showing that LLM/vector spans arrived and not
	// just HTTP ones.
	Operations []string
}

// Result summarises a verification run.
type Result struct {
	Services []ServiceReport
	// Missing are services we instrumented that have not reported yet.
	Missing []string
	// LLMSpans and CostSpans indicate whether the AI-specific instrumentation
	// is working, which is the point of the tool.
	LLMSpans  int
	CostSpans int
	Backend   string // which backend answered, shown to the user
}

// TotalSpans across all reporting services.
func (r Result) TotalSpans() int {
	n := 0
	for _, s := range r.Services {
		n += s.SpanCount
	}
	return n
}

// Backend is a source of truth for "did telemetry arrive".
//
// Two implementations exist because self-hosted and cloud have different access
// paths: ClickHouse needs no credentials but only exists locally, while the API
// works anywhere but needs a key.
type Backend interface {
	// Query returns spans seen in the given window.
	Query(ctx context.Context, since time.Duration) (Result, error)
	// Name identifies the backend in user-facing output.
	Name() string
	// Available reports whether this backend can be used right now.
	Available(ctx context.Context) error
}

// Options controls a verification run.
type Options struct {
	// Expected service names, from detection.
	Expected []string
	// Window to look back over.
	Window time.Duration
	// Timeout for the whole polling loop.
	Timeout time.Duration
	// Interval between polls.
	Interval time.Duration
}

// DefaultOptions are tuned for the gap between a request and a span landing:
// the batch span processor flushes on a schedule and ClickHouse ingests in
// batches, so a few seconds of patience avoids a false negative.
func DefaultOptions(expected []string) Options {
	return Options{
		Expected: expected,
		Window:   5 * time.Minute,
		Timeout:  90 * time.Second,
		Interval: 5 * time.Second,
	}
}

// Wait polls until every expected service reports, or the timeout expires.
//
// Returns the last result even on timeout: partial telemetry is far more useful
// to a user than an error, because it narrows down which service is misconfigured.
func Wait(ctx context.Context, b Backend, opts Options, onTick func(Result)) (Result, error) {
	deadline := time.Now().Add(opts.Timeout)
	var last Result

	for {
		res, err := b.Query(ctx, opts.Window)
		if err != nil {
			return last, err
		}
		res.Backend = b.Name()
		res.Missing = missing(opts.Expected, res.Services)
		last = res

		if onTick != nil {
			onTick(res)
		}
		if len(res.Missing) == 0 && len(res.Services) > 0 {
			return res, nil
		}
		if time.Now().After(deadline) {
			return res, nil
		}

		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}

func missing(expected []string, reporting []ServiceReport) []string {
	seen := make(map[string]bool, len(reporting))
	for _, s := range reporting {
		seen[s.Name] = true
	}
	var out []string
	for _, name := range expected {
		if !seen[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ErrNoBackend is returned when neither verification path is usable.
var ErrNoBackend = fmt.Errorf("no verification backend available")

// SelectBackend picks the first usable backend, preferring the one that needs no
// credentials so the common self-hosted case works with zero configuration.
func SelectBackend(ctx context.Context, backends ...Backend) (Backend, error) {
	var errs []error
	for _, b := range backends {
		if b == nil {
			continue
		}
		if err := b.Available(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Name(), err))
			continue
		}
		return b, nil
	}
	if len(errs) == 0 {
		return nil, ErrNoBackend
	}
	return nil, fmt.Errorf("%w (%v)", ErrNoBackend, errs)
}
