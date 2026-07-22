# signoz-init

Zero-config observability for AI applications. A Go CLI that inspects a Docker Compose
stack, detects each service's language and AI libraries, and wires up full OpenTelemetry
instrumentation — HTTP, DB, LLM spans with token counts, and per-request cost — pointed
at a self-hosted SigNoz.

Built for the Agents of SigNoz hackathon (July 20–26, 2026). Track 01 primary,
Track 02 overlap. Full plan: `~/.claude/plans/lets-plan-it-now-sequential-oasis.md`

## The one-line pitch

Observability should be a `git diff`, not a weekend.

---

## Non-negotiables

These are the invariants. Violating one breaks the product's premise, not just a test.

1. **Never mutate the user's `docker-compose.yml`.** All generated wiring goes to
   `docker-compose.override.yml` (Compose merges it automatically) and `.signoz/`.
   Reverting must be `rm docker-compose.override.yml`, nothing more.

2. **Never write without showing a diff first.** Every file we create or modify gets
   previewed and confirmed. `--dry-run` writes nothing, ever. Users will not accept a
   tool that silently rewrites their infra — and the diff preview is also a demo asset.

3. **Never be silently wrong.** A tool that detects correctly 80% of the time and asks
   cleanly the other 20% beats one that's right 90% and silently wrong 10%. When
   confidence is low, ask. Never guess and proceed quietly.

4. **Verify, don't assume.** After applying, poll SigNoz's API and report actual spans
   received. Any generator can write YAML; confirming telemetry arrived is what makes
   this a product.

---

## Environment (verified 2026-07-22)

| Tool | Version |
|---|---|
| Go | 1.26.5 |
| Docker | 29.6.1 |
| Docker Compose | 5.3.1 |
| Python | 3.12.10 |
| Node | 26.4.0 |
| foundryctl | **not installed** — Day 1 task |

**Host is Linux (Manjaro), not Docker Desktop.** This has one important consequence:

> `host.docker.internal` does **not** resolve by default on Linux Docker Engine.
> Containers reaching the host need either `extra_hosts: ["host.docker.internal:host-gateway"]`
> in the override file, or the host gateway IP directly. **The generator must emit the
> `extra_hosts` mapping** — do not assume Docker Desktop semantics. Alternatively, if
> SigNoz and the app share a Docker network, use the collector's service name instead.

---

## SigNoz facts (researched, not yet hands-on verified)

- **Install:** `curl -fsSL https://signoz.io/foundry.sh | bash`, write a `casting.yaml`,
  then `foundryctl cast -f casting.yaml`. Not raw Compose.
- **Ports:** UI `8080`, OTLP gRPC `4317`, OTLP HTTP `4318`, optional MCP `8000`.
- **It ships its own `signoz/signoz-otel-collector`.** We do not write or fork a
  collector — we point apps at its OTLP endpoint.
- **API:** `POST /api/v2/services` (services + RED metrics), `POST /api/v5/query_range`.
  Auth via API key from Settings → Service Accounts.
- **Native LLM features:** token usage/cost analytics, budget alerts, framework
  dashboards (LangChain, LlamaIndex, CrewAI).

**Open question — resolve on Day 1:** does SigNoz compute cost automatically from
token attributes, or expect a cost attribute we send? If automatic, drop `cost_usd`
from our span processor and pivot the governance layer to alerts only. This determines
whether our cost work is additive or duplicative.

---

## Instrumentation approach

**Reuse, don't rebuild.** We write the *wiring and governance layer*, not instrumentation:
- `traceloop-sdk` (OpenLLMetry) — LLM providers + Qdrant + vector DBs
- `@opentelemetry/auto-instrumentations-node` — Node HTTP/DB
- SigNoz's bundled collector and built-in dashboards

**Node is genuinely zero-touch** — `NODE_OPTIONS="--require ..."` plus env vars in the
override file. Nothing else needed.

**Python is not.** OpenLLMetry requires an explicit `Traceloop.init()`, which breaks the
zero-touch promise. Resolution order, each step explicit and confirmed:
1. `traceloop-sdk` already in the image → mount `sitecustomize.py` on `PYTHONPATH`,
   which Python auto-imports before app code. True zero-touch. (Same pattern the
   OTel Operator uses.)
2. Not installed → offer a diff-previewed `requirements.txt` append + rebuild.
3. `sitecustomize` doesn't fire (unusual entrypoint) → offer an explicit two-line
   edit to the app entrypoint, with a diff.

`assets/sitecustomize.py` is the most fragile file in the repo — it carries both the
zero-touch trick and the cost span processor. Test both the happy path and fallback.

**Semantic conventions:** emit canonical `gen_ai.*` names (`gen_ai.usage.input_tokens`,
`gen_ai.usage.output_tokens`) so SigNoz's built-in dashboards light up for free. These
are still Development/experimental as of 2026 — set `OTEL_SEMCONV_STABILITY_OPT_IN`
for dual-emission.

---

## Layout

```
cmd/signoz-init/       cobra entrypoint
internal/
  detect/              compose parsing, language + AI-library detection
  generate/            override.go is the ONLY thing that writes to user projects
  verify/              polls SigNoz /api/v2/services
  prompt/              interactive confirm UI
assets/                go:embed'd — sitecustomize.py, otel-bootstrap.js,
                       pricing.yaml, dashboards/, alerts/
demo/                  the RAG stack we instrument (also the test fixture)
  api/                 FastAPI: embed → Qdrant search → LLM call
  web/                 Node/Express gateway (proves cross-service propagation)
```

## Detection

Layer weak signals; never trust one. In order of reliability:
`build:` context contents (`requirements.txt`, `package.json`, `pyproject.toml`)
→ image name → `command`/entrypoint string → exposed ports.

AI libraries come from grepping dependency manifests: `openai`, `anthropic`,
`langchain`, `llama-index`, `qdrant-client`, `chromadb`.

Classify as `python`, `node`, `infra:<kind>`, or `unknown`. `unknown` is a valid,
honest outcome — surface it and ask.

## Demo stack

`web` (Node) → `api` (FastAPI) → `qdrant`, with an OpenAI call. One request produces
the exact span tree the hackathon's "Flying Blind" copy describes.

**LLM has two modes.** Real OpenAI when `OPENAI_API_KEY` is set; otherwise a mock
client returning canned answers with realistic token counts. The mock is not
decoration — it means judges can reproduce the demo without their own key, and we can
test the pipeline without burning credits. Both paths must work.

---

## Conventions

- **Go:** standard layout, `cobra` for CLI, `go:embed` for assets. Errors wrapped with
  context (`fmt.Errorf("parsing compose: %w", err)`). No panics in library code.
- **YAML generation:** emit clean, commented, human-readable output. Users will read
  the override file — it's part of the UX, not a build artifact.
- **Output:** concise and scannable. The CLI's feel is a judging criterion (UX and
  Presentation Quality are both scored).
- Do not commit secrets. `OPENAI_API_KEY` comes from the environment or `.env`,
  which stays gitignored.

## Verification

Run `demo/` uninstrumented → `signoz init --dry-run` (assert user's compose file is
byte-identical) → `signoz init` → hit the endpoint → confirm in SigNoz UI a single
trace spanning `web → api → qdrant → openai` with token and cost attributes.

**The credibility test:** run against a Compose app that isn't our demo. If it only
works on the fixture, the pitch is weaker than it sounds. Find that out before stage,
not on it.

Also verify keyless: unset `OPENAI_API_KEY`, rerun, confirm the mock path still
populates traces and cost.

---

## Schedule and cut list

4 days. Day 1 foundation (SigNoz up, hand-wired trace visible — hard gate, nothing
proceeds until a trace lands). Day 2 detect + generate. Day 3 cost + dashboards +
verify. Day 4 harden, generalization test, demo video, submit.

**Cut in this order if short:** alert rule pack → verify polish → Python fallback #3
→ custom dashboards (SigNoz's built-ins cover much of it).

**Never cut:** the demo working end-to-end, or the diff-preview safety.
