package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Eshan276/signoz_hackathon/internal/generate"
	"github.com/spf13/cobra"
)

func newDashboardsCmd() *cobra.Command {
	var (
		apiURL string
		apiKey string
		save   string
	)

	cmd := &cobra.Command{
		Use:   "dashboards",
		Short: "Install the LLM cost and token dashboard into SigNoz",
		Long: `Installs a dashboard covering LLM spend, token usage by model, call
latency, and vector-search latency.

Costs are read from the gen_ai.usage.cost_usd attribute that the instrumentation
attaches, so no pricing is baked into the dashboard queries — editing
.signoz/python/pricing.yaml is enough to change what the dashboard reports.

Importing needs an API key (Settings → Service Accounts). Without one, use
--save to write the JSON out and import it through the UI instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := os.Stdout

			payload, err := generate.Dashboard(generate.DashboardAssets[0].Embedded)
			if err != nil {
				return err
			}

			if save != "" {
				if err := os.WriteFile(save, payload, 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", save, err)
				}
				fmt.Fprintf(out, "Wrote %s\n", save)
				fmt.Fprintf(out, "Import it in SigNoz: Dashboards → New → Import JSON\n")
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			imp := &generate.DashboardImporter{BaseURL: apiURL, APIKey: apiKey}
			path, err := imp.Import(ctx, payload)
			if err != nil {
				fmt.Fprintf(out, "Could not import automatically: %v\n\n", err)
				fmt.Fprintf(out, "Write it to a file instead and import through the UI:\n")
				fmt.Fprintf(out, "  signoz-init dashboards --save llm-cost.json\n")
				return nil
			}

			fmt.Fprintf(out, "Dashboard installed via %s\n", path)
			fmt.Fprintf(out, "Open %s and look for \"LLM Cost & Token Usage\".\n", apiURL)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&apiURL, "api-url", "http://localhost:8080", "SigNoz base URL")
	f.StringVar(&apiKey, "api-key", os.Getenv("SIGNOZ_API_KEY"),
		"SigNoz API key; also read from SIGNOZ_API_KEY")
	f.StringVar(&save, "save", "",
		"write the dashboard JSON to a file instead of importing it")

	return cmd
}
