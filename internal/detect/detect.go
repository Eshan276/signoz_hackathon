package detect

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Runtime is what a service runs on, which decides how we inject instrumentation.
type Runtime string

const (
	RuntimePython  Runtime = "python"
	RuntimeNode    Runtime = "node"
	RuntimeInfra   Runtime = "infra"
	RuntimeUnknown Runtime = "unknown"
)

// Confidence records how sure we are, so the UI knows when to ask rather than
// state. A tool that is right 80% of the time and asks cleanly beats one that is
// right 90% and silently wrong the rest.
type Confidence int

const (
	ConfidenceLow Confidence = iota
	ConfidenceMedium
	ConfidenceHigh
)

func (c Confidence) String() string {
	switch c {
	case ConfidenceHigh:
		return "high"
	case ConfidenceMedium:
		return "medium"
	default:
		return "low"
	}
}

// Service is the detection result for one compose service.
type Service struct {
	Name         string
	Runtime      Runtime
	Framework    string   // fastapi, express, django, ...
	InfraKind    string   // qdrant, postgres, redis, ... when Runtime is infra
	AILibs       []string // openai, langchain, qdrant-client, ...
	Confidence   Confidence
	Reasons      []string // why we concluded this; shown with --explain
	Instrument   bool     // whether we will wire this service up
	BuildContext string   // absolute path to the build context, "" if image-only
}

// infraImages maps well-known image name fragments to a kind. These are services
// we observe via the collector rather than inject into.
var infraImages = map[string]string{
	"qdrant":        "qdrant",
	"postgres":      "postgres",
	"mysql":         "mysql",
	"mariadb":       "mysql",
	"redis":         "redis",
	"mongo":         "mongodb",
	"clickhouse":    "clickhouse",
	"elasticsearch": "elasticsearch",
	"rabbitmq":      "rabbitmq",
	"kafka":         "kafka",
	"nginx":         "nginx",
	"traefik":       "traefik",
	"chroma":        "chromadb",
	"weaviate":      "weaviate",
	"milvus":        "milvus",
	"minio":         "minio",
	"memcached":     "memcached",
}

// pythonAILibs and nodeAILibs are dependency names worth reporting, because their
// presence is what makes a service interesting for LLM observability.
var pythonAILibs = []string{
	"openai", "anthropic", "langchain", "llama-index", "llama_index",
	"qdrant-client", "chromadb", "pinecone", "weaviate-client", "cohere",
	"mistralai", "google-generativeai", "boto3", "ollama", "transformers",
	"crewai", "haystack", "litellm",
}

var nodeAILibs = []string{
	"openai", "@anthropic-ai/sdk", "langchain", "llamaindex",
	"@qdrant/js-client-rest", "chromadb", "@pinecone-database/pinecone",
	"weaviate-ts-client", "cohere-ai", "ollama",
}

// DetectAll classifies every service in the compose file. baseDir is the
// directory containing the compose file, used to resolve relative build contexts.
func DetectAll(c *Compose, baseDir string) []Service {
	names := make([]string, 0, len(c.Services))
	for name := range c.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Service, 0, len(names))
	for _, name := range names {
		s := detectService(name, c.Services[name], baseDir)
		// Default to instrumenting anything we can inject into. Infra is observed
		// through the apps that call it; unknown services are left alone rather
		// than guessed at. The user can override either way at the confirm step.
		s.Instrument = s.Runtime == RuntimePython || s.Runtime == RuntimeNode
		out = append(out, s)
	}
	return out
}

func detectService(name string, svc ComposeService, baseDir string) Service {
	s := Service{Name: name, Runtime: RuntimeUnknown, Confidence: ConfidenceLow}

	// 1. Infrastructure by image name. Highest signal and cheapest to check.
	if svc.Image != "" {
		img := strings.ToLower(svc.Image)
		for frag, kind := range infraImages {
			if strings.Contains(img, frag) {
				s.Runtime = RuntimeInfra
				s.InfraKind = kind
				s.Confidence = ConfidenceHigh
				s.Reasons = append(s.Reasons, "image "+svc.Image+" matches known "+kind)
				return s
			}
		}
	}

	// 2. Build context contents. The strongest signal for app services, because
	// a manifest file is unambiguous in a way an image name never is.
	if ctx := buildContext(svc, baseDir); ctx != "" {
		s.BuildContext = ctx
		if r := detectFromBuildContext(&s, ctx); r != RuntimeUnknown {
			s.Runtime = r
			s.Confidence = ConfidenceHigh
			return s
		}
	}

	// 3. Image name for language runtimes (python:3.12, node:22-slim).
	if svc.Image != "" {
		img := strings.ToLower(svc.Image)
		switch {
		case strings.HasPrefix(img, "python") || strings.Contains(img, "/python"):
			s.Runtime = RuntimePython
			s.Confidence = ConfidenceMedium
			s.Reasons = append(s.Reasons, "image "+svc.Image+" is a Python base image")
			return s
		case strings.HasPrefix(img, "node") || strings.Contains(img, "/node"):
			s.Runtime = RuntimeNode
			s.Confidence = ConfidenceMedium
			s.Reasons = append(s.Reasons, "image "+svc.Image+" is a Node base image")
			return s
		}
	}

	// 4. Command or entrypoint. Weakest signal, so it only yields medium/low.
	cmd := strings.ToLower(strings.Join(append(append([]string{}, svc.Entrypoint...), svc.Command...), " "))
	switch {
	case strings.Contains(cmd, "uvicorn"), strings.Contains(cmd, "gunicorn"),
		strings.Contains(cmd, "python"), strings.Contains(cmd, "flask"),
		strings.Contains(cmd, "manage.py"), strings.Contains(cmd, "celery"):
		s.Runtime = RuntimePython
		s.Confidence = ConfidenceMedium
		s.Reasons = append(s.Reasons, "command mentions a Python runtime")
		if strings.Contains(cmd, "uvicorn") {
			s.Framework = "fastapi"
		}
	case strings.Contains(cmd, "node "), strings.Contains(cmd, "npm "),
		strings.Contains(cmd, "yarn "), strings.Contains(cmd, "pnpm "),
		strings.HasSuffix(cmd, ".js"), strings.Contains(cmd, "nest"):
		s.Runtime = RuntimeNode
		s.Confidence = ConfidenceMedium
		s.Reasons = append(s.Reasons, "command mentions a Node runtime")
	}

	if s.Runtime == RuntimeUnknown {
		s.Reasons = append(s.Reasons, "no build context, recognisable image, or command signal")
	}
	return s
}

// buildContext resolves a service's build context to an absolute path, or "" if
// the service has no build section.
func buildContext(svc ComposeService, baseDir string) string {
	if svc.Build.Context == "" {
		return ""
	}
	if filepath.IsAbs(svc.Build.Context) {
		return svc.Build.Context
	}
	return filepath.Join(baseDir, svc.Build.Context)
}

// detectFromBuildContext looks for dependency manifests, which also tell us which
// AI libraries are in play.
func detectFromBuildContext(s *Service, ctx string) Runtime {
	// Python
	for _, f := range []string{"requirements.txt", "pyproject.toml", "Pipfile", "setup.py"} {
		path := filepath.Join(ctx, f)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.ToLower(string(data))
		s.Reasons = append(s.Reasons, "found "+f+" in build context")
		s.AILibs = matchLibs(content, pythonAILibs)
		s.Framework = pythonFramework(content)
		return RuntimePython
	}

	// Node
	if data, err := os.ReadFile(filepath.Join(ctx, "package.json")); err == nil {
		content := strings.ToLower(string(data))
		s.Reasons = append(s.Reasons, "found package.json in build context")
		s.AILibs = matchLibs(content, nodeAILibs)
		s.Framework = nodeFramework(content)
		return RuntimeNode
	}

	return RuntimeUnknown
}

func pythonFramework(content string) string {
	switch {
	case strings.Contains(content, "fastapi"):
		return "fastapi"
	case strings.Contains(content, "django"):
		return "django"
	case strings.Contains(content, "flask"):
		return "flask"
	case strings.Contains(content, "starlette"):
		return "starlette"
	}
	return ""
}

func nodeFramework(content string) string {
	switch {
	case strings.Contains(content, "\"next\""):
		return "nextjs"
	case strings.Contains(content, "@nestjs/core"):
		return "nestjs"
	case strings.Contains(content, "\"express\""):
		return "express"
	case strings.Contains(content, "\"fastify\""):
		return "fastify"
	case strings.Contains(content, "\"koa\""):
		return "koa"
	}
	return ""
}

func matchLibs(content string, candidates []string) []string {
	var found []string
	for _, lib := range candidates {
		if strings.Contains(content, lib) {
			found = append(found, lib)
		}
	}
	sort.Strings(found)
	return found
}
