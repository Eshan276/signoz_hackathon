package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ClickHouseBackend queries SigNoz's telemetry store directly through the
// container, via `docker exec`.
//
// Reaching into SigNoz's storage is not something a product should normally do,
// but for a locally self-hosted stack it is the only path that needs no
// credentials — and requiring an API key before you can see your first span
// would undercut the whole "one command" premise. The API backend covers cloud.
type ClickHouseBackend struct {
	// Container running clickhouse-server. Discovered automatically when empty.
	Container string
}

func (c *ClickHouseBackend) Name() string { return "clickhouse (self-hosted)" }

// defaultContainerCandidates covers the naming foundryctl produces.
var defaultContainerCandidates = []string{
	"signoz-telemetrystore-clickhouse-0-0",
	"signoz-clickhouse-1",
	"clickhouse",
}

func (c *ClickHouseBackend) Available(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH")
	}
	if c.Container != "" {
		return c.ping(ctx, c.Container)
	}

	for _, name := range defaultContainerCandidates {
		if err := c.ping(ctx, name); err == nil {
			c.Container = name
			return nil
		}
	}

	// Fall back to discovery, so a renamed stack still works.
	name, err := c.discover(ctx)
	if err != nil {
		return err
	}
	c.Container = name
	return c.ping(ctx, name)
}

func (c *ClickHouseBackend) discover(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "ancestor=clickhouse/clickhouse-server",
		"--format", "{{.Names}}").Output()
	if err != nil {
		return "", fmt.Errorf("listing containers: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return "", fmt.Errorf("no ClickHouse container running")
	}
	return names[0], nil
}

func (c *ClickHouseBackend) ping(ctx context.Context, container string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", container,
		"clickhouse-client", "--query", "SELECT 1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cannot query %s: %s", container, strings.TrimSpace(string(out)))
	}
	return nil
}

// spanIndexTable is SigNoz's trace index. Pinned by name because the schema is
// versioned; if SigNoz moves to v4 this is the single line to change.
const spanIndexTable = "signoz_traces.distributed_signoz_index_v3"

func (c *ClickHouseBackend) Query(ctx context.Context, since time.Duration) (Result, error) {
	var res Result

	minutes := int(since.Minutes())
	if minutes < 1 {
		minutes = 1
	}

	// One query for the per-service rollup, including a sample of operation
	// names so the user can see that LLM/vector spans arrived, not just HTTP.
	q := fmt.Sprintf(`
		SELECT serviceName,
		       count() AS spans,
		       arraySlice(arrayDistinct(groupArray(name)), 1, 12) AS ops
		FROM %s
		WHERE timestamp > now() - INTERVAL %d MINUTE
		GROUP BY serviceName
		ORDER BY spans DESC
		FORMAT JSONEachRow`, spanIndexTable, minutes)

	out, err := c.run(ctx, q)
	if err != nil {
		return res, err
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			ServiceName string   `json:"serviceName"`
			Spans       any      `json:"spans"`
			Ops         []string `json:"ops"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue // a malformed row should not fail the whole verification
		}
		res.Services = append(res.Services, ServiceReport{
			Name:       row.ServiceName,
			SpanCount:  asInt(row.Spans),
			Operations: row.Ops,
		})
	}

	// Separately count the AI-specific spans, since those are the ones this tool
	// exists to produce.
	// Match both the newer gen_ai.usage.input_tokens/output_tokens names and the
	// older prompt_tokens/completion_tokens ones. The real OpenAI instrumentor
	// still emits the older pair, so checking only the new names reports zero LLM
	// spans against a live provider even though cost attribution worked.
	llmQuery := fmt.Sprintf(`
		SELECT
		  countIf(mapContains(attributes_number, 'gen_ai.usage.input_tokens')
		          OR mapContains(attributes_number, 'gen_ai.usage.output_tokens')
		          OR mapContains(attributes_number, 'gen_ai.usage.prompt_tokens')
		          OR mapContains(attributes_number, 'gen_ai.usage.completion_tokens')) AS llm,
		  countIf(mapContains(attributes_number, 'gen_ai.usage.cost_usd')) AS cost
		FROM %s
		WHERE timestamp > now() - INTERVAL %d MINUTE
		FORMAT JSONEachRow`, spanIndexTable, minutes)

	if out, err := c.run(ctx, llmQuery); err == nil {
		var row struct {
			LLM  any `json:"llm"`
			Cost any `json:"cost"`
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			if json.Unmarshal([]byte(line), &row) == nil {
				res.LLMSpans = asInt(row.LLM)
				res.CostSpans = asInt(row.Cost)
			}
		}
	}

	return res, nil
}

func (c *ClickHouseBackend) run(ctx context.Context, query string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", c.Container,
		"clickhouse-client", "--query", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("clickhouse query failed: %s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// asInt handles ClickHouse returning counts as either JSON numbers or strings
// depending on the column type.
func asInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}
