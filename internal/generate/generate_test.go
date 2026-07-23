package generate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eshan/signoz-init/internal/detect"
)

func TestOverrideIncludesRequiredWiring(t *testing.T) {
	services := []detect.Service{
		{Name: "api", Runtime: detect.RuntimePython, Framework: "fastapi", Instrument: true},
		{Name: "web", Runtime: detect.RuntimeNode, Framework: "express", Instrument: true},
		{Name: "qdrant", Runtime: detect.RuntimeInfra, InfraKind: "qdrant"},
	}
	out := Override(services, DefaultConfig())

	// Each of these was a Day 1 failure mode; losing any one silently breaks
	// telemetry with no error, so assert them explicitly.
	required := []string{
		"PYTHONPATH: /otel",  // zero-touch python injection
		"TRACELOOP_BASE_URL", // api_endpoint kwarg alone exports nowhere
		"NODE_OPTIONS: --require /otel/otel-bootstrap.js", // zero-touch node injection
		"host.docker.internal:host-gateway",               // linux docker engine
		"OTEL_SERVICE_NAME: api",
		"OTEL_SERVICE_NAME: web",
	}
	for _, want := range required {
		if !strings.Contains(out, want) {
			t.Errorf("override missing %q", want)
		}
	}

	// Infra is observed through its callers, never injected into.
	if strings.Contains(out, "\n  qdrant:") {
		t.Error("infra service should not appear in the override")
	}
}

func TestOverrideHandlesNoInstrumentableServices(t *testing.T) {
	out := Override([]detect.Service{
		{Name: "db", Runtime: detect.RuntimeInfra, InfraKind: "postgres"},
	}, DefaultConfig())

	if !strings.Contains(out, "No instrumentable services") {
		t.Errorf("expected an explanatory comment, got:\n%s", out)
	}
}

func TestPythonDepEditAddsFrameworkInstrumentor(t *testing.T) {
	dir := t.TempDir()
	reqs := filepath.Join(dir, "requirements.txt")
	if err := os.WriteFile(reqs, []byte("fastapi==0.115.6\nuvicorn==0.34.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	edits, err := PlanDepEdits([]detect.Service{{
		Name: "api", Runtime: detect.RuntimePython, Framework: "fastapi",
		Instrument: true, BuildContext: dir,
	}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	// Traceloop does not instrument web frameworks; without this the trace has
	// no root span and every LLM span is orphaned.
	if !strings.Contains(edits[0].Updated, "opentelemetry-instrumentation-fastapi") {
		t.Error("expected the FastAPI instrumentor to be added")
	}
	if !strings.Contains(edits[0].Updated, "traceloop-sdk") {
		t.Error("expected traceloop-sdk to be added")
	}
	// Existing content must survive untouched.
	if !strings.Contains(edits[0].Updated, "fastapi==0.115.6") {
		t.Error("original requirements were not preserved")
	}
}

func TestDepEditSkippedWhenPackagesPresent(t *testing.T) {
	dir := t.TempDir()
	content := "fastapi==0.115.6\ntraceloop-sdk==0.38.7\nopentelemetry-distro==0.65b0\n" +
		"opentelemetry-instrumentation-fastapi==0.65b0\n"
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	edits, err := PlanDepEdits([]detect.Service{{
		Name: "api", Runtime: detect.RuntimePython, Framework: "fastapi",
		Instrument: true, BuildContext: dir,
	}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 0 {
		t.Errorf("expected no edits when deps already present, got %d", len(edits))
	}
}

func TestNodeDepEditProducesValidJSON(t *testing.T) {
	dir := t.TempDir()
	original := `{
  "name": "web",
  "dependencies": {
    "express": "4.21.2"
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	edits, err := PlanDepEdits([]detect.Service{{
		Name: "web", Runtime: detect.RuntimeNode, Instrument: true, BuildContext: dir,
	}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	var parsed struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(edits[0].Updated), &parsed); err != nil {
		t.Fatalf("generated package.json is not valid JSON: %v\n%s", err, edits[0].Updated)
	}
	if parsed.Dependencies["express"] != "4.21.2" {
		t.Error("existing dependency was lost")
	}
	for _, pkg := range nodePackages {
		if parsed.Dependencies[pkg] == "" {
			t.Errorf("missing %s", pkg)
		}
	}
}

// Appending to an already-broken manifest turns a visible problem into a hidden
// one, so we refuse rather than compound it.
func TestNodeDepEditRefusesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	broken := "{\n  \"dependencies\": {\n    \"express\": \"4.21.2\",\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := PlanDepEdits([]detect.Service{{
		Name: "web", Runtime: detect.RuntimeNode, Instrument: true, BuildContext: dir,
	}}, dir)
	if err == nil {
		t.Fatal("expected an error for malformed package.json")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("error should explain the problem, got: %v", err)
	}
}

// An image-only service has nowhere to add packages; that must not be an error.
func TestImageOnlyServiceProducesNoEdit(t *testing.T) {
	edits, err := PlanDepEdits([]detect.Service{{
		Name: "api", Runtime: detect.RuntimePython, Instrument: true, BuildContext: "",
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 0 {
		t.Errorf("expected no edits without a build context, got %d", len(edits))
	}
}

func TestAssetsForSelectsByRuntime(t *testing.T) {
	assets := AssetsFor([]detect.Service{
		{Name: "api", Runtime: detect.RuntimePython, Instrument: true},
		{Name: "db", Runtime: detect.RuntimeInfra, InfraKind: "postgres"},
	})

	var haveNode bool
	for _, a := range assets {
		if strings.Contains(a.Target, "node") {
			haveNode = true
		}
	}
	if haveNode {
		t.Error("node assets should not be emitted for a python-only stack")
	}
	if len(assets) == 0 {
		t.Error("expected python assets")
	}
}

// The embedded payloads are what actually instrument the app; a missing or
// truncated one fails at runtime inside a container, which is hard to debug.
func TestEmbeddedAssetsArePresent(t *testing.T) {
	for _, a := range append(append([]Asset{}, PythonAssets...), NodeAssets...) {
		data, err := Read(a.Embedded)
		if err != nil {
			t.Errorf("%s: %v", a.Embedded, err)
			continue
		}
		if len(data) < 100 {
			t.Errorf("%s: suspiciously small (%d bytes)", a.Embedded, len(data))
		}
	}
}
