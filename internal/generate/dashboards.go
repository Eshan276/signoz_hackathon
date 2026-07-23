package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DashboardAssets are the dashboards `signoz init` can install.
var DashboardAssets = []Asset{
	{"files/dashboards/llm-cost.json", "dashboards/llm-cost.json"},
}

// Dashboard returns a named dashboard's JSON.
func Dashboard(embedded string) ([]byte, error) { return Read(embedded) }

// DashboardImporter pushes dashboards into SigNoz over its API.
type DashboardImporter struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (d *DashboardImporter) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// dashboardEndpoints are tried in order. SigNoz has both v1 and v2 dashboard
// routes depending on version, and rather than sniffing the version we try the
// newer one first and fall back.
var dashboardEndpoints = []string{"/api/v2/dashboards", "/api/v1/dashboards"}

// Import creates a dashboard in SigNoz, returning the endpoint that accepted it.
func (d *DashboardImporter) Import(ctx context.Context, payload []byte) (string, error) {
	if d.APIKey == "" {
		return "", fmt.Errorf("an API key is required to import dashboards " +
			"(create one under Settings → Service Accounts, then pass --api-key " +
			"or set SIGNOZ_API_KEY)")
	}

	var lastErr error
	for _, path := range dashboardEndpoints {
		url := strings.TrimRight(d.BaseURL, "/") + path

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("SIGNOZ-API-KEY", d.APIKey)
		req.Header.Set("Authorization", "Bearer "+d.APIKey)

		resp, err := d.client().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", path, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return path, nil
		}
		// 404 means this version does not have that route; anything else is a
		// real failure worth surfacing rather than silently retrying.
		if resp.StatusCode != http.StatusNotFound {
			return "", fmt.Errorf("%s returned %s: %s", path, resp.Status, summarise(body))
		}
		lastErr = fmt.Errorf("%s: not found", path)
	}
	return "", fmt.Errorf("could not import dashboard: %w", lastErr)
}

// summarise pulls the message out of SigNoz's error envelope so users see the
// actual reason instead of a wall of JSON.
func summarise(body []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
