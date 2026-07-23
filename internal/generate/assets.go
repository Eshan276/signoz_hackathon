// Package generate turns detection results into the files that wire a stack up:
// a compose override, the language bootstraps, and the pricing table.
//
// Everything here writes to docker-compose.override.yml and .signoz/ only. The
// user's own compose file is never modified.
package generate

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// Assets are baked into the binary so `signoz init` works with no network access
// and no companion files to install.
//
//go:embed all:files
var assets embed.FS

// AssetDir is where generated payloads land, relative to the project root.
const AssetDir = ".signoz"

// Asset is one embedded file and where it should be written.
type Asset struct {
	Embedded string // path within the embedded FS
	Target   string // path relative to the project root
}

// PythonAssets are mounted into Python services at /otel.
var PythonAssets = []Asset{
	{"files/sitecustomize.py", filepath.Join(AssetDir, "python", "sitecustomize.py")},
	{"files/pricing.yaml", filepath.Join(AssetDir, "python", "pricing.yaml")},
}

// NodeAssets are mounted into Node services at /otel.
var NodeAssets = []Asset{
	{"files/otel-bootstrap.js", filepath.Join(AssetDir, "node", "otel-bootstrap.js")},
}

// Read returns the contents of an embedded asset.
func Read(embedded string) ([]byte, error) {
	data, err := assets.ReadFile(embedded)
	if err != nil {
		return nil, fmt.Errorf("reading embedded asset %s: %w", embedded, err)
	}
	return data, nil
}

// Write materialises an asset under root, creating parent directories.
func Write(root string, a Asset) error {
	data, err := Read(a.Embedded)
	if err != nil {
		return err
	}
	dest := filepath.Join(root, a.Target)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}
