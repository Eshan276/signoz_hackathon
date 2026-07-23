package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFixture builds a throwaway project on disk. Detection reads real files
// (requirements.txt, package.json), so fixtures have to be real too.
func writeFixture(t *testing.T, compose string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func detectFixture(t *testing.T, compose string, files map[string]string) map[string]Service {
	t.Helper()
	dir := writeFixture(t, compose, files)

	path, err := FindComposeFile(dir)
	if err != nil {
		t.Fatalf("FindComposeFile: %v", err)
	}
	c, err := ParseCompose(path)
	if err != nil {
		t.Fatalf("ParseCompose: %v", err)
	}

	out := map[string]Service{}
	for _, s := range DetectAll(c, dir) {
		out[s.Name] = s
	}
	return out
}

func TestDetectFastAPIFromRequirements(t *testing.T) {
	svcs := detectFixture(t, `
services:
  api:
    build: ./api
`, map[string]string{
		"api/requirements.txt": "fastapi==0.115.6\nuvicorn==0.34.0\nopenai==1.59\nqdrant-client==1.12\n",
	})

	api := svcs["api"]
	if api.Runtime != RuntimePython {
		t.Errorf("runtime = %q, want python", api.Runtime)
	}
	if api.Framework != "fastapi" {
		t.Errorf("framework = %q, want fastapi", api.Framework)
	}
	if api.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %v, want high", api.Confidence)
	}
	if !api.Instrument {
		t.Error("expected api to be selected for instrumentation")
	}

	wantLibs := map[string]bool{"openai": true, "qdrant-client": true}
	for _, lib := range api.AILibs {
		delete(wantLibs, lib)
	}
	if len(wantLibs) > 0 {
		t.Errorf("missing AI libs %v (got %v)", wantLibs, api.AILibs)
	}
}

func TestDetectExpressFromPackageJSON(t *testing.T) {
	svcs := detectFixture(t, `
services:
  web:
    build:
      context: ./web
      dockerfile: Dockerfile
`, map[string]string{
		"web/package.json": `{"dependencies":{"express":"4.21.2","openai":"4.0.0"}}`,
	})

	web := svcs["web"]
	if web.Runtime != RuntimeNode {
		t.Errorf("runtime = %q, want node", web.Runtime)
	}
	if web.Framework != "express" {
		t.Errorf("framework = %q, want express", web.Framework)
	}
	// Long-form build: context must still resolve, or dep editing breaks.
	if web.BuildContext == "" {
		t.Error("expected BuildContext to be set for long-form build")
	}
}

func TestDetectInfraByImage(t *testing.T) {
	svcs := detectFixture(t, `
services:
  qdrant:
    image: qdrant/qdrant:v1.12.5
  db:
    image: postgres:16
  cache:
    image: redis:7-alpine
`, nil)

	for name, wantKind := range map[string]string{
		"qdrant": "qdrant", "db": "postgres", "cache": "redis",
	} {
		s := svcs[name]
		if s.Runtime != RuntimeInfra {
			t.Errorf("%s: runtime = %q, want infra", name, s.Runtime)
		}
		if s.InfraKind != wantKind {
			t.Errorf("%s: kind = %q, want %q", name, s.InfraKind, wantKind)
		}
		if s.Instrument {
			t.Errorf("%s: infra should not be instrumented directly", name)
		}
	}
}

// An opaque image with no other signal must be reported as unknown rather than
// guessed at. Being honestly uncertain is the designed behaviour.
func TestUnknownServiceIsNotGuessed(t *testing.T) {
	svcs := detectFixture(t, `
services:
  mystery:
    image: some-vendor/blackbox:latest
`, nil)

	m := svcs["mystery"]
	if m.Runtime != RuntimeUnknown {
		t.Errorf("runtime = %q, want unknown", m.Runtime)
	}
	if m.Confidence != ConfidenceLow {
		t.Errorf("confidence = %v, want low", m.Confidence)
	}
	if m.Instrument {
		t.Error("unknown services must not be instrumented silently")
	}
	if len(m.Reasons) == 0 {
		t.Error("expected a reason explaining why detection failed")
	}
}

// Command strings are a weaker signal than a manifest, so they should yield
// medium confidence — enough to act on, but flagged in the UI.
func TestCommandSignalGivesMediumConfidence(t *testing.T) {
	svcs := detectFixture(t, `
services:
  legacy:
    image: myregistry.io/internal/app:v2
    command: python manage.py runserver
  gateway:
    image: node:22-alpine
    command: ["node", "index.js"]
`, nil)

	if got := svcs["legacy"]; got.Runtime != RuntimePython || got.Confidence != ConfidenceMedium {
		t.Errorf("legacy = %q/%v, want python/medium", got.Runtime, got.Confidence)
	}
	if got := svcs["gateway"]; got.Runtime != RuntimeNode {
		t.Errorf("gateway = %q, want node", got.Runtime)
	}
}

// Compose allows several shapes for the same field; all must parse.
func TestComposeFieldVariants(t *testing.T) {
	svcs := detectFixture(t, `
services:
  a:
    image: python:3.12
    environment:
      - FOO=bar
      - BAZ=qux
    command: python -m http.server
    depends_on:
      - b
  b:
    image: node:22
    environment:
      FOO: bar
    entrypoint: ["node"]
    command: ["server.js"]
`, nil)

	if len(svcs) != 2 {
		t.Fatalf("expected 2 services, got %d", len(svcs))
	}
	if svcs["a"].Runtime != RuntimePython {
		t.Errorf("a: runtime = %q, want python", svcs["a"].Runtime)
	}
	if svcs["b"].Runtime != RuntimeNode {
		t.Errorf("b: runtime = %q, want node", svcs["b"].Runtime)
	}
}

func TestMissingComposeFileIsAnError(t *testing.T) {
	if _, err := FindComposeFile(t.TempDir()); err == nil {
		t.Fatal("expected an error when no compose file exists")
	}
}
