package prompt

import (
	"fmt"
	"io"
	"strings"

	"github.com/eshan/signoz-init/internal/detect"
)

// Summary prints what we found and what we intend to do about it.
//
// Low-confidence detections are flagged rather than presented as fact — the
// point is that the user can correct us before anything is written.
func Summary(w io.Writer, services []detect.Service, explain, color bool) {
	p := newPalette(color)

	fmt.Fprintf(w, "\n%sDetected services%s\n\n", p.bold, p.reset)

	var uncertain int
	for _, s := range services {
		mark := " "
		markColor := ""
		switch {
		case s.Instrument:
			mark, markColor = "✓", p.green
		case s.Runtime == detect.RuntimeInfra:
			mark, markColor = "·", p.dim
		default:
			mark, markColor = "?", p.yellow
		}

		fmt.Fprintf(w, "  %s%s%s %-14s %s\n",
			markColor, mark, p.reset, s.Name, describe(s, p))

		if len(s.AILibs) > 0 {
			fmt.Fprintf(w, "      %sAI libraries: %s%s\n",
				p.cyan, strings.Join(s.AILibs, ", "), p.reset)
		}
		if s.Confidence == detect.ConfidenceLow {
			uncertain++
		}
		if explain {
			for _, r := range s.Reasons {
				fmt.Fprintf(w, "      %s· %s%s\n", p.dim, r, p.reset)
			}
		}
	}

	legend := fmt.Sprintf("\n  %s✓%s instrument   %s·%s observed via callers   %s?%s skipped\n",
		p.green, p.reset, p.dim, p.reset, p.yellow, p.reset)
	fmt.Fprint(w, legend)

	if uncertain > 0 && !explain {
		fmt.Fprintf(w, "\n  %s%d service(s) could not be identified confidently. "+
			"Re-run with --explain to see why.%s\n", p.dim, uncertain, p.reset)
	}
}

func describe(s detect.Service, p palette) string {
	switch s.Runtime {
	case detect.RuntimeInfra:
		return fmt.Sprintf("%s%s%s", p.dim, s.InfraKind, p.reset)
	case detect.RuntimeUnknown:
		return fmt.Sprintf("%sunknown%s", p.yellow, p.reset)
	}

	label := string(s.Runtime)
	if s.Framework != "" {
		label += " / " + s.Framework
	}
	if s.Confidence != detect.ConfidenceHigh {
		label += fmt.Sprintf(" %s(%s confidence)%s", p.dim, s.Confidence, p.reset)
	}
	return label
}
