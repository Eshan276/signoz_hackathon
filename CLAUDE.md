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

## SigNoz facts (hands-on verified 2026-07-22)

- **Install:** `curl -fsSL https://signoz.io/foundry.sh | bash` → `foundryctl gen examples`
  → copy `docs/examples/docker/compose/casting.yaml` → `foundryctl cast`.
  Installed **foundryctl v0.2.16**. The `casting.yaml` is only 8 lines
  (`kind: Installation`, `flavor: compose`, `mode: docker`).
- **Ports:** UI `8080`, OTLP gRPC `4317`, OTLP HTTP `4318` — all published to the host.
- **Docker network:** `signoz-network`, compose project name `signoz`.
- **It ships its own `signoz/signoz-otel-collector`** (container `signoz-ingester-1`).
- **API:** `POST /api/v2/services`, `POST /api/v5/query_range`.
- **Native LLM features:** token usage/cost analytics, budget alerts, framework
  dashboards (LangChain, LlamaIndex, CrewAI).

### ⚠️ First-run gotcha — OTLP is CLOSED until an org exists

Freshly cast, the ingester logs `cannot create agent without orgId` and **never opens
4317/4318**. The OpAMP server refuses to hand it a config until an organization exists,
which only happens after first-run signup. Connections are refused, not rejected —
`curl` returns exit 56 / code 000, which looks like a networking bug and is not one.

Fix — register the first admin:
```bash
curl -X POST http://localhost:8080/api/v1/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"admin","orgName":"hackathon","email":"<email>","password":"<pw>"}'
```
OTLP opens within ~30s and returns `200`.

**This is a product requirement, not just a local workaround.** "SigNoz is running" ≠
"SigNoz can accept telemetry." `signoz init` must detect the no-org state and guide the
user through it, otherwise the first thing a new user sees is a silent black hole.
Local dev creds: `eshandas2002@gmail.com` / `Signoz@12345` (throwaway; never commit).

**Startup is slow.** ClickHouse schema migrations take several minutes on first boot.
Poll for readiness; never assume the stack is up because containers are "running".

**RESOLVED 2026-07-22 — our cost layer is additive, not duplicative.** SigNoz tracks
token counts but does **not** compute dollar cost. Its LLM dashboards use input/output
tokens as a cost *proxy* and expect you to scale by your own per-model pricing; the
official sample cost dashboard does exactly that by hand. There is no built-in pricing
table. So emitting `gen_ai.usage.cost_usd` from `assets/pricing.yaml` fills a real gap,
and the cost-governance layer stays in scope as planned.

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

### Hard-won Day 1 findings (all verified 2026-07-22)

Each of these cost real debugging time. They are the reason `signoz init` is worth
building — a normal user hits them and gives up.

1. **`TRACELOOP_BASE_URL` is what routes traces, not the `api_endpoint` kwarg.**
   `Traceloop.init(api_endpoint=...)` alone exports nowhere, silently. Set the env var.

2. **Traceloop does NOT instrument web frameworks.** It covers LLM providers, vector
   DBs, and HTTP *clients* (requests, urllib3) — but not FastAPI/Flask/Django. Without
   `opentelemetry-instrumentation-fastapi` there is no root server span and every LLM
   span is an orphan. `opentelemetry-distro` + the framework instrumentor are required.

3. **Do not double-instrument.** Calling `_load_instrumentors()` after `Traceloop.init()`
   logs "Attempting to instrument while already instrumented" and *breaks the existing
   Qdrant patch*. Only add what Traceloop lacks.

4. **Traceloop's own AI instrumentor init silently fails.** Its `init_*_instrumentor()`
   functions swallow every exception. Qdrant never got patched and nothing was logged.
   Call the instrumentors explicitly (`QdrantInstrumentor().instrument()`), guarded by
   `is_instrumented_by_opentelemetry`.

5. **`async def` endpoints + `run_in_threadpool` lose OTel context** → orphaned root
   spans. A plain `def` endpoint works, because Starlette runs it via anyio's
   `to_thread.run_sync`, which copies contextvars. Counter-intuitive but verified both ways.

6. **Cost cannot be attached in `SpanProcessor.on_end`.** By then the span is a
   ReadableSpan and the exporter has already captured attributes — the value never
   leaves the process (exports as 0). Wrap the *exporter* instead and mutate just
   before serialisation.

7. **`BatchSpanProcessor.span_exporter` is read-only.** Assign through the backing
   field `proc._batch_processor._exporter`.

8. **`rebuild, don't restart`.** The demo Dockerfiles `COPY` source in, so
   `docker compose restart` runs stale code. Several confusing results traced back to
   this. Mounted assets (`/otel`) do pick up changes on restart.

9. **OpenLLMetry's Qdrant instrumentor wraps `search`/`query` but NOT `query_points`.**
   The modern Qdrant API produces no span. Also: construct clients lazily — a client
   built at import time can capture unpatched methods.

10. **Traceloop never sets the GLOBAL tracer provider.** It keeps its own internally,
    so `trace.get_tracer()` returns a `ProxyTracerProvider` and every hand-written
    span is a `NonRecordingSpan` that silently vanishes. Instrumented libraries emit
    spans while the user's own code emits nothing, with no error. `sitecustomize`
    promotes Traceloop's provider to global via `_promote_global_tracer_provider()`.

### SigNoz API routes (extracted from the frontend bundle, 2026-07-23)

`/api/v1/login` does **not** exist — unknown `/api/*` paths fall through to the SPA
and return HTML with a 200, which makes probing misleading. Discriminator: a real
route returns JSON (often a 401), the SPA returns `<!doctype html>`.

Confirmed real: `/api/v2/services`, `/api/v2/users`, `/api/v2/sessions/rotate`,
`/api/v1/dashboards`, `/api/v2/dashboards`, `/api/v2/rules`, `/api/v5/query_range`,
`/api/v1/service_accounts`, `/api/v1/register` (disabled after first user).

**`/api/v1/llm_pricing_rules`** (+ `/unmapped_models`) exists but is undocumented —
SigNoz maps models to prices server-side. Our `pricing.yaml` should be pushed here so
both cost views agree.

Route list: `docker exec signoz-signoz-0 sh -c 'grep -rhoE "/api/v[0-9]+/[a-zA-Z_/]+" /etc/signoz/web/assets/*.js | sort -u'`

---

## Layout

```
cmd/signoz-init/       cobra entrypoint; init command, phases 1-3
internal/
  detect/              compose.go (parsing), detect.go (heuristics) + tests
  generate/            assets.go (go:embed), override.go, deps.go + tests
    files/             EMBEDDED PAYLOADS — sitecustomize.py, otel-bootstrap.js,
                       pricing.yaml. go:embed cannot reach outside the package,
                       so these live here rather than in a top-level assets/.
  verify/              polls SigNoz /api/v2/services (Day 3)
  prompt/              summary.go (detection table), diff.go (LCS diff + confirm)
demo/                  the RAG stack we instrument (also the test fixture)
  api/                 FastAPI: embed → Qdrant search → LLM call
  web/                 Node/Express gateway (proves cross-service propagation)
infra/                 foundryctl casting.yaml for the local SigNoz stack
```

Build: `go build -o bin/signoz-init ./cmd/signoz-init`   Test: `go test ./internal/...`

## CLI surface (working as of Day 3)

```
signoz-init init [dir] [--dry-run] [--explain] [--yes] [--endpoint URL]
                       [--no-verify] [--api-url URL] [--api-key KEY] [--no-color]
signoz-init dashboards [--save FILE] [--api-url URL] [--api-key KEY]
```

`--dry-run` prints the full diff and exits without writing. `--explain` shows why
each service was classified the way it was.

**Verification has two backends.** `ClickHouseBackend` shells into the ClickHouse
container — no credentials, works for self-hosted, and is preferred. `APIBackend`
uses `/api/v2/services` with an API key and works against Cloud. `SelectBackend`
picks the first available; if neither is, verification is skipped with an
explanation rather than failing the command (the files are already correct).

Live check: `SIGNOZ_LIVE_TEST=1 go test -count=1 ./internal/verify/ -v`

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

**LLM has two modes.** Real provider when `OPENAI_API_KEY` is set; otherwise a mock
client returning canned answers with realistic token counts. The mock is not
decoration — it means judges can reproduce the demo without their own key, and we can
test the pipeline without burning credits. Both paths must work.

**The real path is provider-agnostic via `LLM_BASE_URL`.** It uses the OpenAI SDK,
which speaks to any OpenAI-compatible endpoint. Verified 2026-07-23 against **Gemini**
(`https://generativelanguage.googleapis.com/v1beta/openai/`, model `gemini-2.5-flash`):
real `openai.chat` span, real tokens, `cost_usd` attached. Because it is the OpenAI SDK
on the wire, OpenLLMetry's OpenAI instrumentor produces the span regardless of provider.
Config lives in the gitignored `demo/.env` (`env_file: required: false`).

**Real OpenAI spans use the OLDER token attribute names** — `gen_ai.usage.prompt_tokens`
/ `completion_tokens`, not `input_tokens` / `output_tokens`. `_compute_cost` reads both,
but the verify query and dashboards keyed only on the new names. Fix: the cost exporter
now also copies the old names to the canonical ones, so every downstream consumer sees a
single set. Check this whenever adding a new provider.

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

**Day 1 — DONE.** SigNoz up via foundryctl; full distributed trace
(`web → api → qdrant.search`) with zero app code changes; cost attribution verified
(1000 in + 500 out on gpt-4o-mini → `gen_ai.usage.cost_usd = 0.00045`).

**Day 2 — DONE.** Go CLI detects, generates, diffs, and applies. Verified on a clean
copy the CLI had never seen: 6 files written, user's `docker-compose.yml` byte-identical,
trace reproduced, `rm docker-compose.override.yml` fully reverts. 16 unit tests green.

**Day 3 — DONE.** Verify phase (dual backend), LLM cost dashboard (9 widgets, v5
schema) + `dashboards` import command. Mock LLM now emits proper `gen_ai.*` spans so
the demo works keyless. Full e2e on a fresh project: `llm spans=7, cost spans=7`,
trace = `web → api → qdrant.search → chat gpt-4o-mini` with `cost_usd` attached.

**Alert rule pack: DEFERRED.** SigNoz's rule JSON schema is undocumented and the
frontend bundle does not expose it. It was first on the cut list, and the dashboard
carries most of the governance value. Revisit only if Day 4 has slack.

**Day 4** harden, generalization test against a third-party stack, README, demo
video, submit.

**Cut in this order if short:** alert rule pack → verify polish → Python fallback #3
→ custom dashboards (SigNoz's built-ins cover much of it).

**Never cut:** the demo working end-to-end, or the diff-preview safety.
