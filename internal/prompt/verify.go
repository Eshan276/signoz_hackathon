package prompt

import (
	"fmt"
	"io"
	"strings"

	"github.com/Eshan276/signoz_hackathon/internal/verify"
)

// VerifyResult reports what telemetry actually arrived.
//
// Shows LLM and cost span counts separately from the overall total, because a
// stack can be reporting HTTP spans perfectly while the AI instrumentation —
// the reason this tool exists — is silently doing nothing.
func VerifyResult(w io.Writer, res verify.Result, color bool) {
	p := newPalette(color)

	if len(res.Services) == 0 {
		fmt.Fprintf(w, "\n%sNo telemetry received yet.%s\n", p.yellow, p.reset)
		fmt.Fprintf(w, "\nCommon causes:\n")
		fmt.Fprintf(w, "  · the stack was not restarted (docker compose up -d --build)\n")
		fmt.Fprintf(w, "  · no traffic has been sent to the app yet\n")
		fmt.Fprintf(w, "  · the images lack instrumentation packages — check container logs\n")
		fmt.Fprintf(w, "    for lines beginning [signoz-init]\n")
		return
	}

	fmt.Fprintf(w, "\n%s✓ %d service(s) reporting, %d spans%s\n",
		p.green, len(res.Services), res.TotalSpans(), p.reset)

	for _, s := range res.Services {
		fmt.Fprintf(w, "\n  %s%s%s  %d spans\n", p.bold, s.Name, p.reset, s.SpanCount)
		if len(s.Operations) > 0 {
			ops := s.Operations
			const maxShown = 6
			suffix := ""
			if len(ops) > maxShown {
				suffix = fmt.Sprintf(" (+%d more)", len(ops)-maxShown)
				ops = ops[:maxShown]
			}
			fmt.Fprintf(w, "    %s%s%s%s\n", p.dim, strings.Join(ops, ", "), suffix, p.reset)
		}
	}

	if res.LLMSpans > 0 {
		fmt.Fprintf(w, "\n  %s%d LLM span(s) with token counts%s\n",
			p.cyan, res.LLMSpans, p.reset)
		if res.CostSpans > 0 {
			fmt.Fprintf(w, "  %s%d span(s) with cost attribution%s\n",
				p.cyan, res.CostSpans, p.reset)
		} else {
			fmt.Fprintf(w, "  %sno cost attribution yet — check that the model appears "+
				"in .signoz/python/pricing.yaml%s\n", p.dim, p.reset)
		}
	}

	if len(res.Missing) > 0 {
		fmt.Fprintf(w, "\n  %sNot reporting yet: %s%s\n",
			p.yellow, strings.Join(res.Missing, ", "), p.reset)
		fmt.Fprintf(w, "  %sSend traffic to those services, or check their logs for "+
			"[signoz-init] lines.%s\n", p.dim, p.reset)
	}
}
