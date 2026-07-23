package generate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eshan/signoz-init/internal/detect"
)

// Instrumentation packages we need present inside the image. The bootstraps are
// mounted from outside, but the SDKs they import must be installed in the image.
var (
	// pythonPackages: traceloop-sdk covers LLM providers and vector DBs but NOT
	// web frameworks, so the distro and framework instrumentor are both required
	// — without them there is no root server span and LLM spans are orphaned.
	pythonPackages = []string{
		"traceloop-sdk",
		"opentelemetry-distro",
	}

	nodePackages = []string{
		"@opentelemetry/sdk-node",
		"@opentelemetry/auto-instrumentations-node",
		"@opentelemetry/exporter-trace-otlp-proto",
	}
)

// frameworkPackage maps a detected Python framework to its OTel instrumentor.
var frameworkPackage = map[string]string{
	"fastapi":   "opentelemetry-instrumentation-fastapi",
	"starlette": "opentelemetry-instrumentation-fastapi",
	"flask":     "opentelemetry-instrumentation-flask",
	"django":    "opentelemetry-instrumentation-django",
}

// DepEdit is a proposed change to a dependency manifest. Nothing is applied
// until the user sees the diff and confirms.
type DepEdit struct {
	Service  string
	Path     string   // absolute path to the manifest
	Rel      string   // path shown to the user
	Missing  []string // packages to add
	Original string
	Updated  string
}

// PlanDepEdits works out which services are missing instrumentation packages.
//
// Returns only services that need changes; a service whose image already has the
// packages needs no edit and stays truly zero-touch.
func PlanDepEdits(services []detect.Service, baseDir string) ([]DepEdit, error) {
	var edits []DepEdit

	for _, s := range services {
		if !s.Instrument {
			continue
		}
		switch s.Runtime {
		case detect.RuntimePython:
			edit, err := planPythonEdit(s, baseDir)
			if err != nil {
				return nil, err
			}
			if edit != nil {
				edits = append(edits, *edit)
			}
		case detect.RuntimeNode:
			edit, err := planNodeEdit(s, baseDir)
			if err != nil {
				return nil, err
			}
			if edit != nil {
				edits = append(edits, *edit)
			}
		}
	}
	return edits, nil
}

func planPythonEdit(s detect.Service, baseDir string) (*DepEdit, error) {
	if s.BuildContext == "" {
		// No build context: we cannot add packages, so the bootstrap will detect
		// the missing SDK at runtime and explain what to do.
		return nil, nil
	}
	path := filepath.Join(s.BuildContext, "requirements.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // no requirements.txt; nothing we can safely edit
	}

	existing := strings.ToLower(string(data))
	want := append([]string{}, pythonPackages...)
	if pkg, ok := frameworkPackage[s.Framework]; ok {
		want = append(want, pkg)
	}

	var missing []string
	for _, pkg := range want {
		if !strings.Contains(existing, strings.ToLower(pkg)) {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}

	original := string(data)
	var b strings.Builder
	b.WriteString(original)
	if !strings.HasSuffix(original, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n# Added by `signoz init` — OpenTelemetry instrumentation\n")
	for _, pkg := range missing {
		b.WriteString(pkg + "\n")
	}

	rel, _ := filepath.Rel(baseDir, path)
	return &DepEdit{
		Service:  s.Name,
		Path:     path,
		Rel:      rel,
		Missing:  missing,
		Original: original,
		Updated:  b.String(),
	}, nil
}

func planNodeEdit(s detect.Service, baseDir string) (*DepEdit, error) {
	if s.BuildContext == "" {
		return nil, nil
	}
	path := filepath.Join(s.BuildContext, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}

	existing := string(data)

	// Refuse to touch a manifest we cannot parse. Appending to a file that is
	// already malformed turns a problem the user can see into one they cannot.
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf(
			"service %q: %s is not valid JSON (%v); fix it and re-run, or add these "+
				"packages manually: %s",
			s.Name, filepath.Base(path), err, strings.Join(nodePackages, ", "))
	}

	var missing []string
	for _, pkg := range nodePackages {
		if !strings.Contains(existing, "\""+pkg+"\"") {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}

	updated, err := addNodeDeps(existing, missing)
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", s.Name, err)
	}

	rel, _ := filepath.Rel(baseDir, path)
	return &DepEdit{
		Service:  s.Name,
		Path:     path,
		Rel:      rel,
		Missing:  missing,
		Original: existing,
		Updated:  updated,
	}, nil
}

// otelVersions pins the versions we know work together. Mixing OTel JS package
// versions causes subtle API mismatches, so they are pinned rather than floating.
var otelVersions = map[string]string{
	"@opentelemetry/sdk-node":                   "0.57.0",
	"@opentelemetry/auto-instrumentations-node": "0.56.0",
	"@opentelemetry/exporter-trace-otlp-proto":  "0.57.0",
}

// addNodeDeps inserts packages into the dependencies block textually, preserving
// the rest of the file byte-for-byte. Round-tripping through encoding/json would
// reorder keys and drop formatting in a file the user has to read.
func addNodeDeps(content string, missing []string) (string, error) {
	idx := strings.Index(content, "\"dependencies\"")
	if idx == -1 {
		return "", fmt.Errorf("package.json has no \"dependencies\" block")
	}
	brace := strings.Index(content[idx:], "{")
	if brace == -1 {
		return "", fmt.Errorf("malformed \"dependencies\" block")
	}
	insertAt := idx + brace + 1

	indent := "    "
	var b strings.Builder
	for _, pkg := range missing {
		version := otelVersions[pkg]
		if version == "" {
			version = "latest"
		}
		fmt.Fprintf(&b, "\n%s\"%s\": \"%s\",", indent, pkg, version)
	}

	return content[:insertAt] + b.String() + content[insertAt:], nil
}
