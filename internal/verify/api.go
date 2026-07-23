package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// APIBackend queries SigNoz's HTTP API.
//
// Works against both self-hosted and SigNoz Cloud, but needs a key: create one
// under Settings → Service Accounts and pass it via --api-key or SIGNOZ_API_KEY.
type APIBackend struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (a *APIBackend) Name() string { return "signoz api" }

func (a *APIBackend) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (a *APIBackend) Available(ctx context.Context) error {
	if a.APIKey == "" {
		return fmt.Errorf("no API key (set SIGNOZ_API_KEY or pass --api-key)")
	}
	if a.BaseURL == "" {
		return fmt.Errorf("no base URL")
	}

	// A cheap authenticated call to confirm the key is actually accepted, rather
	// than discovering that mid-verification.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(a.BaseURL, "/")+"/api/v1/dashboards", nil)
	if err != nil {
		return err
	}
	a.authorize(req)

	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", a.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("API key rejected (%s)", resp.Status)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("SigNoz returned %s", resp.Status)
	}
	return nil
}

// authorize sets both accepted header styles. SigNoz has used SIGNOZ-API-KEY for
// service-account keys and bearer tokens for sessions; sending both avoids
// guessing which one this deployment wants.
func (a *APIBackend) authorize(req *http.Request) {
	req.Header.Set("SIGNOZ-API-KEY", a.APIKey)
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Content-Type", "application/json")
}

func (a *APIBackend) Query(ctx context.Context, since time.Duration) (Result, error) {
	var res Result

	end := time.Now()
	start := end.Add(-since)

	body, _ := json.Marshal(map[string]any{
		"start": start.UnixNano(),
		"end":   end.UnixNano(),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(a.BaseURL, "/")+"/api/v2/services", bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	a.authorize(req)

	resp, err := a.client().Do(req)
	if err != nil {
		return res, fmt.Errorf("querying services: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("services API returned %s", resp.Status)
	}

	// SigNoz wraps payloads in {"status":..,"data":..}; the service list shape has
	// moved around across versions, so decode defensively rather than assuming.
	var envelope struct {
		Data []struct {
			ServiceName string `json:"serviceName"`
			NumCalls    int    `json:"numCalls"`
			NumErrors   int    `json:"numErrors"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return res, fmt.Errorf("decoding services response: %w", err)
	}

	for _, s := range envelope.Data {
		if s.ServiceName == "" {
			continue
		}
		res.Services = append(res.Services, ServiceReport{
			Name:      s.ServiceName,
			SpanCount: s.NumCalls,
		})
	}
	return res, nil
}
