// Command signoz-init wires OpenTelemetry instrumentation into a Docker Compose
// stack — including LLM and vector-DB spans with token counts and cost — without
// editing the user's compose file or application code.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Eshan276/signoz_hackathon/internal/detect"
	"github.com/Eshan276/signoz_hackathon/internal/generate"
	"github.com/Eshan276/signoz_hackathon/internal/prompt"
	"github.com/Eshan276/signoz_hackathon/internal/verify"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "signoz-init",
		Short:   "Zero-config OpenTelemetry instrumentation for Docker Compose stacks",
		Version: version,
		Long: `signoz-init inspects a Docker Compose project, works out what each service
runs, and wires up OpenTelemetry — HTTP, database, LLM and vector-DB spans with
token counts and per-request cost — pointed at SigNoz.

Your docker-compose.yml is never modified. Everything is written to
docker-compose.override.yml, which Compose merges automatically, so removing the
instrumentation is a single "rm".`,
	}
	root.AddCommand(newInitCmd())
	root.AddCommand(newDashboardsCmd())
	return root
}

type initOptions struct {
	dir      string
	endpoint string
	dryRun   bool
	explain  bool
	yes      bool
	noColor  bool
	noVerify bool
	apiURL   string
	apiKey   string
}

func newInitCmd() *cobra.Command {
	opts := initOptions{}

	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Instrument the Compose stack in the given directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.dir = "."
			if len(args) == 1 {
				opts.dir = args[0]
			}
			return runInit(opts)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.endpoint, "endpoint", generate.DefaultConfig().Endpoint,
		"OTLP HTTP endpoint to export telemetry to")
	f.BoolVar(&opts.dryRun, "dry-run", false,
		"show what would change and exit without writing anything")
	f.BoolVar(&opts.explain, "explain", false,
		"show why each service was classified the way it was")
	f.BoolVarP(&opts.yes, "yes", "y", false,
		"skip confirmation prompts (assumes yes)")
	f.BoolVar(&opts.noColor, "no-color", false, "disable coloured output")
	f.BoolVar(&opts.noVerify, "no-verify", false,
		"skip checking that telemetry actually arrives")
	f.StringVar(&opts.apiURL, "api-url", "http://localhost:8080",
		"SigNoz UI/API base URL, used for verification")
	f.StringVar(&opts.apiKey, "api-key", os.Getenv("SIGNOZ_API_KEY"),
		"SigNoz API key (Settings → Service Accounts); also read from SIGNOZ_API_KEY")

	return cmd
}

func runInit(opts initOptions) error {
	color := !opts.noColor && os.Getenv("NO_COLOR") == "" &&
		term.IsTerminal(int(os.Stdout.Fd()))
	out := os.Stdout

	root, err := filepath.Abs(opts.dir)
	if err != nil {
		return err
	}

	// 1. Detect
	composePath, err := detect.FindComposeFile(root)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Reading %s\n", relTo(root, composePath))

	compose, err := detect.ParseCompose(composePath)
	if err != nil {
		return err
	}
	services := detect.DetectAll(compose, filepath.Dir(composePath))

	// 2. Confirm what we found
	prompt.Summary(out, services, opts.explain, color)

	if countInstrumented(services) == 0 {
		return fmt.Errorf("no instrumentable services found (only Python and Node are supported today)")
	}

	// 3. Build the full set of proposed changes before writing any of them.
	cfg := generate.Config{Endpoint: opts.endpoint, UseHostGateway: true}
	changes, err := planChanges(root, services, cfg)
	if err != nil {
		return err
	}

	prompt.RenderDiff(out, changes, color)

	if opts.dryRun {
		fmt.Fprintf(out, "\nDry run — nothing was written.\n")
		return nil
	}

	if !opts.yes {
		ok, err := prompt.Confirm(os.Stdin, out, "Apply these changes?", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Aborted. Nothing was written.")
			return nil
		}
	}

	// 4. Apply
	if err := applyChanges(changes); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%d file(s) written.\n", len(changes))

	// 5. Verify — the step that turns a generator into something trustworthy.
	if opts.noVerify {
		printNextSteps(out)
		return nil
	}
	return verifyPhase(opts, services, out, color)
}

// verifyPhase waits for telemetry to actually show up in SigNoz and reports what
// arrived. Never fails the command: the files are already written and correct, so
// a verification problem is information, not an error.
func verifyPhase(opts initOptions, services []detect.Service, out *os.File, color bool) error {
	expected := make([]string, 0, len(services))
	for _, s := range services {
		if s.Instrument {
			expected = append(expected, s.Name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	backend, err := verify.SelectBackend(ctx,
		&verify.ClickHouseBackend{},
		&verify.APIBackend{BaseURL: opts.apiURL, APIKey: opts.apiKey},
	)
	if err != nil {
		fmt.Fprintf(out, "\nSkipping verification: %v\n", err)
		printNextSteps(out)
		return nil
	}

	// Offer to rebuild the stack ourselves. The instrumentation only takes effect
	// after a rebuild (the images COPY source in), so making the user switch
	// terminals to run this is exactly the friction the tool exists to remove.
	ok, err := prompt.Confirm(os.Stdin, out,
		"Rebuild and restart the stack now (docker compose up -d --build)?", true)
	if err != nil {
		return err
	}
	if !ok {
		printNextSteps(out)
		return nil
	}

	if err := composeUp(opts.dir, out); err != nil {
		fmt.Fprintf(out, "\nCould not rebuild automatically: %v\n", err)
		printNextSteps(out)
		return nil
	}

	fmt.Fprintf(out, "\nWatching via %s for up to %s...\n",
		backend.Name(), verify.DefaultOptions(nil).Timeout)
	fmt.Fprintf(out, "Send your app a request in another terminal if it has no traffic yet.\n")

	res, err := verify.Wait(ctx, backend, verify.DefaultOptions(expected), nil)
	if err != nil {
		fmt.Fprintf(out, "Verification stopped: %v\n", err)
		printNextSteps(out)
		return nil
	}

	prompt.VerifyResult(out, res, color)
	fmt.Fprintf(out, "\nDashboard: %s\n", opts.apiURL)
	fmt.Fprintf(out, "To undo everything: rm %s\n", generate.OverrideFilename)
	return nil
}

func printNextSteps(out *os.File) {
	fmt.Fprintf(out, "\nNext:\n")
	fmt.Fprintf(out, "  docker compose up -d --build\n")
	fmt.Fprintf(out, "\nThen send traffic to your app and check SigNoz.\n")
	fmt.Fprintf(out, "To undo everything: rm %s\n", generate.OverrideFilename)
}

// composeUp runs `docker compose up -d --build` in the project directory,
// streaming its output so the user sees the rebuild rather than a silent hang.
func composeUp(dir string, out *os.File) error {
	fmt.Fprintf(out, "\nRebuilding — this can take a minute...\n\n")

	cmd := exec.Command("docker", "compose", "up", "-d", "--build")
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up failed: %w", err)
	}
	return nil
}

// planChanges assembles every file we intend to write, so the user sees the
// complete picture in one diff rather than a sequence of prompts.
func planChanges(root string, services []detect.Service, cfg generate.Config) ([]prompt.FileChange, error) {
	var changes []prompt.FileChange

	overridePath := filepath.Join(root, generate.OverrideFilename)
	existing := ""
	if data, err := os.ReadFile(overridePath); err == nil {
		existing = string(data)
	}
	changes = append(changes, prompt.FileChange{
		Path:     generate.OverrideFilename,
		AbsPath:  overridePath,
		Original: existing,
		Updated:  generate.Override(services, cfg),
		Note:     "merged by Compose automatically; your docker-compose.yml is untouched",
	})

	for _, a := range generate.AssetsFor(services) {
		data, err := generate.Read(a.Embedded)
		if err != nil {
			return nil, err
		}
		abs := filepath.Join(root, a.Target)
		prior := ""
		if b, err := os.ReadFile(abs); err == nil {
			prior = string(b)
		}
		changes = append(changes, prompt.FileChange{
			Path:     a.Target,
			AbsPath:  abs,
			Original: prior,
			Updated:  string(data),
			Note:     "mounted read-only into the container at /otel",
		})
	}

	edits, err := generate.PlanDepEdits(services, root)
	if err != nil {
		return nil, err
	}
	for _, e := range edits {
		changes = append(changes, prompt.FileChange{
			Path:     e.Rel,
			AbsPath:  e.Path,
			Original: e.Original,
			Updated:  e.Updated,
			Note: fmt.Sprintf("service %q needs these packages inside its image; requires a rebuild",
				e.Service),
		})
	}

	return changes, nil
}

func applyChanges(changes []prompt.FileChange) error {
	for _, c := range changes {
		// Paths in changes are relative to the project root, and planChanges
		// built them from absolute roots, so resolve against the same base.
		if err := os.MkdirAll(filepath.Dir(c.AbsPath), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", c.Path, err)
		}
		if err := os.WriteFile(c.AbsPath, []byte(c.Updated), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", c.Path, err)
		}
	}
	return nil
}

func countInstrumented(services []detect.Service) int {
	n := 0
	for _, s := range services {
		if s.Instrument {
			n++
		}
	}
	return n
}

func relTo(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return rel
	}
	return path
}
