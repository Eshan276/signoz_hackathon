// Package detect inspects a Docker Compose project and works out what each
// service is, so instrumentation can be wired up without the user describing
// their stack by hand.
package detect

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Compose is a deliberately partial view of a compose file. We only model the
// fields that inform detection — anything else is left untouched, because we
// never rewrite the user's compose file, only read it.
type Compose struct {
	Name     string                    `yaml:"name"`
	Services map[string]ComposeService `yaml:"services"`
}

type ComposeService struct {
	Image       string      `yaml:"image"`
	Build       BuildConfig `yaml:"build"`
	Command     StringOrSlice `yaml:"command"`
	Entrypoint  StringOrSlice `yaml:"entrypoint"`
	Ports       []string    `yaml:"ports"`
	Environment Environment `yaml:"environment"`
	DependsOn   StringOrSlice `yaml:"depends_on"`
}

// BuildConfig handles both `build: ./dir` and the long form with a context key.
type BuildConfig struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

func (b *BuildConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		b.Context = value.Value
		return nil
	}
	type raw BuildConfig
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*b = BuildConfig(r)
	return nil
}

// StringOrSlice accepts the scalar and sequence forms compose allows for
// command, entrypoint, and depends_on.
type StringOrSlice []string

func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = []string{value.Value}
	case yaml.SequenceNode:
		var items []string
		if err := value.Decode(&items); err != nil {
			return err
		}
		*s = items
	case yaml.MappingNode:
		// depends_on long form: keys are service names.
		var m map[string]any
		if err := value.Decode(&m); err != nil {
			return err
		}
		for k := range m {
			*s = append(*s, k)
		}
	}
	return nil
}

// Environment accepts both the map and `KEY=value` list forms.
type Environment map[string]string

func (e *Environment) UnmarshalYAML(value *yaml.Node) error {
	out := Environment{}
	switch value.Kind {
	case yaml.MappingNode:
		var m map[string]string
		if err := value.Decode(&m); err != nil {
			return err
		}
		for k, v := range m {
			out[k] = v
		}
	case yaml.SequenceNode:
		var items []string
		if err := value.Decode(&items); err != nil {
			return err
		}
		for _, item := range items {
			for i := 0; i < len(item); i++ {
				if item[i] == '=' {
					out[item[:i]] = item[i+1:]
					break
				}
			}
		}
	}
	*e = out
	return nil
}

// composeFilenames are tried in the order Docker Compose itself uses.
var composeFilenames = []string{
	"docker-compose.yml",
	"docker-compose.yaml",
	"compose.yml",
	"compose.yaml",
}

// FindComposeFile locates the compose file in dir, returning its full path.
func FindComposeFile(dir string) (string, error) {
	for _, name := range composeFilenames {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %s (looked for %v)", dir, composeFilenames)
}

// ParseCompose reads and parses a compose file.
func ParseCompose(path string) (*Compose, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading compose file: %w", err)
	}

	var c Compose
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filepath.Base(path), err)
	}
	if len(c.Services) == 0 {
		return nil, fmt.Errorf("%s defines no services", filepath.Base(path))
	}
	return &c, nil
}
