package verify

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestClickHouseBackendLive exercises the real backend against a running SigNoz.
//
// Skipped unless SIGNOZ_LIVE_TEST=1, so `go test ./...` stays hermetic. This is
// the only way to catch a schema or container-naming change, which unit tests
// with fake data would sail straight past.
func TestClickHouseBackendLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live test skipped in short mode")
	}
	if os.Getenv("SIGNOZ_LIVE_TEST") != "1" {
		t.Skip("set SIGNOZ_LIVE_TEST=1 to run against a live SigNoz stack")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b := &ClickHouseBackend{}
	if err := b.Available(ctx); err != nil {
		t.Fatalf("backend unavailable: %v", err)
	}
	t.Logf("using container %q", b.Container)

	res, err := b.Query(ctx, 10*time.Minute)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Services) == 0 {
		t.Fatal("no services reporting; send traffic to the demo stack first")
	}

	for _, s := range res.Services {
		t.Logf("service %s: %d spans, ops=%v", s.Name, s.SpanCount, s.Operations)
		if s.SpanCount == 0 {
			t.Errorf("service %s reported with zero spans", s.Name)
		}
	}
	t.Logf("llm spans=%d cost spans=%d", res.LLMSpans, res.CostSpans)
}
