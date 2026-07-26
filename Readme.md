# signoz-init

**Zero-config observability for AI applications.**
Point it at a Docker Compose stack and it wires up full OpenTelemetry
instrumentation — HTTP, database, **LLM and vector-DB spans with token counts and
per-request cost** — into SigNoz. One command, no application code changes, your
`docker-compose.yml` never touched.

> Observability should be a `git diff`, not a weekend.

Built for the [Agents of SigNoz hackathon](https://www.wemakedevs.org/hackathons/signoz)
(Track 01 · AI & Agent Observability).

---

## The problem

The hackathon calls it *Flying Blind*:

> AI agents are chaining LLM calls, invoking tools, hitting vector DBs, and making
> decisions autonomously. But when latency spikes, costs explode, or an agent
> hallucinates in production, you're flying blind.

The telemetry isn't impossible — it's just tedious enough that most teams never wire
it up. Auto-instrumentation for a Python/Node stack, plus LLM and vector-DB spans,
plus a collector, plus cost math, plus dashboards, is a weekend of yak-shaving before
the first span appears. `signoz-init` makes it one command.

---

## What you get

From a request through a RAG service, this exact trace — with **zero app code changes**:

```
web:POST /ask                      ← Node/Express gateway
└─ api:POST /ask                   ← FastAPI (root server span)
   ├─ api:qdrant.search            ← vector DB
   └─ api:openai.chat              ← LLM call
      ├─ gen_ai.usage.input_tokens : 77
      ├─ gen_ai.usage.output_tokens: 25
      └─ gen_ai.usage.cost_usd     : 0.0000856   ← in dollars, not tokens
```

That last line is the point. SigNoz tracks token *counts* natively but does not
compute dollar cost — its own sample dashboards scale tokens by hand. `signoz-init`
emits `gen_ai.usage.cost_usd` from an editable pricing table, filling the gap.

---

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/Eshan276/signoz_hackathon/main/install.sh | sh
```

No Go toolchain needed — this pulls a prebuilt binary for your platform. It lands in
`~/.local/bin`; add that to your `PATH` if it isn't already.

<details>
<summary>Other ways to install</summary>

```bash
# With Go
go install github.com/Eshan276/signoz_hackathon/cmd/signoz-init@latest

# From source
git clone https://github.com/Eshan276/signoz_hackathon && cd signoz_hackathon
go build -o bin/signoz-init ./cmd/signoz-init
```

Windows: download the `.exe` from the [Releases](https://github.com/Eshan276/signoz_hackathon/releases) page.
</details>

## Quick start

```bash
# 1. Stand up SigNoz (once) — see "Running SigNoz" below
# 2. Point signoz-init at your Compose project
signoz-init init ./demo
```

It shows you exactly what it will change, waits for confirmation, applies, then
**polls SigNoz and reports the spans that actually arrived**:

```
✓ 2 service(s) reporting, 168 spans

  api  123 spans
    POST /ask, qdrant.search, openai.chat, GET /health
  web  45 spans
    POST /ask, middleware - expressInit, tcp.connect

  7 LLM span(s) with token counts
  7 span(s) with cost attribution
```

Undo everything with a single `rm docker-compose.override.yml`.

---

## How it works

Five phases, each visible and confirmable:

| Phase | What happens |
|-------|--------------|
| **Detect** | Parses `docker-compose.yml`, classifies each service by layered signals — build-context manifests → image name → command → ports. Reports `unknown` honestly rather than guessing. |
| **Confirm** | Shows a detection table. Low-confidence guesses are flagged, not stated. |
| **Generate** | Writes `docker-compose.override.yml` + `.signoz/` assets. **Shows a full diff first.** |
| **Apply** | `docker compose up -d --build`. |
| **Verify** | Polls SigNoz until your services report, then prints what arrived. |

### The injection trick (no code changes)

- **Node** is genuinely zero-touch: `NODE_OPTIONS=--require /otel/otel-bootstrap.js`
  plus env vars, all in the override file.
- **Python** mounts a `sitecustomize.py` on `PYTHONPATH`, which the interpreter
  imports automatically before your app — the same mechanism the OpenTelemetry
  Operator uses. It installs the instrumentation and the cost processor.

Instrumentation packages must exist *inside* the image. If they're missing,
`signoz-init` offers a diff-previewed edit to `requirements.txt` / `package.json` and
a rebuild — never a silent change.

### What we build vs. reuse

We write the **wiring and governance layer**, not instrumentation. Under the hood:
[OpenLLMetry](https://github.com/traceloop/openllmetry) for LLM providers and vector
DBs, [`@opentelemetry/auto-instrumentations-node`](https://www.npmjs.com/package/@opentelemetry/auto-instrumentations-node)
for Node, and SigNoz's bundled collector and dashboards.

---

## The four invariants

These are non-negotiable — each one is what makes the tool trustworthy enough to run
against real infrastructure:

1. **Your `docker-compose.yml` is never modified.** Everything lands in
   `docker-compose.override.yml`, which Compose merges automatically.
2. **Nothing is written without a diff.** `--dry-run` writes nothing, ever.
3. **Never silently wrong.** When confidence is low, it asks. `unknown` is a valid,
   honest result.
4. **Verify, don't assume.** It confirms telemetry actually arrived — any tool can
   write YAML.

---

## Usage

```
signoz-init init [dir] [flags]
  --dry-run          show the full diff and exit without writing
  --explain          show why each service was classified as it was
  --yes, -y          skip confirmation prompts
  --endpoint URL     OTLP endpoint (default http://host.docker.internal:4318)
  --no-verify        skip the telemetry check
  --api-url URL      SigNoz base URL for verification (default http://localhost:8080)
  --api-key KEY      SigNoz API key (or set SIGNOZ_API_KEY); needed only for cloud

signoz-init dashboards [flags]
  --save FILE        write the LLM cost dashboard JSON instead of importing it
  --api-key KEY      import directly (Settings → Service Accounts)
```

### Example: preview without touching anything

```console
$ signoz-init init ./demo --dry-run

Reading docker-compose.yml

Detected services

  ✓ api            python / fastapi
      AI libraries: openai, qdrant-client
  · qdrant         qdrant
  ✓ web            node / express

  ✓ instrument   · observed via callers   ? skipped

create docker-compose.override.yml
+   api:
+     environment:
+       PYTHONPATH: /otel
+       OTEL_SERVICE_NAME: api
+       TRACELOOP_BASE_URL: http://host.docker.internal:4318
      ...

Dry run — nothing was written.
```

---

## The demo stack

`demo/` is a real RAG service, and doubles as the test fixture:

- **web** (Node/Express) — a gateway, proving cross-service trace propagation.
- **api** (FastAPI) — `POST /ask`: embed → Qdrant search → build prompt → LLM call.
- **qdrant** — the vector store, seeded on startup.

**Runs with or without an API key.** With no key it uses a mock LLM that emits proper
`gen_ai.*` spans with realistic token counts, so the whole pipeline — including cost —
works for anyone. With a key it calls a real model.

The real path works with **any OpenAI-compatible provider** via `LLM_BASE_URL`. To
run against Gemini, create `demo/.env` (gitignored):

```bash
OPENAI_API_KEY=<your-gemini-key>
LLM_BASE_URL=https://generativelanguage.googleapis.com/v1beta/openai/
LLM_MODEL=gemini-2.5-flash
```

---

## Running SigNoz

Self-hosted via [foundryctl](https://signoz.io):

```bash
curl -fsSL https://signoz.io/foundry.sh | bash
cd infra && foundryctl cast          # uses the casting.yaml here
```

UI on `:8080`, OTLP on `:4317`/`:4318`.

> **First-run gotcha:** freshly cast, SigNoz keeps its OTLP ports **closed** until an
> organization exists. Register the first admin (UI signup, or
> `POST /api/v1/register`) and OTLP opens within ~30s. "Running" ≠ "accepting
> telemetry" — this is one of the failure modes `signoz-init` exists to smooth over.

---

## LLM cost dashboard

```bash
signoz-init dashboards --api-key <key>       # import directly
signoz-init dashboards --save llm-cost.json  # or import via the UI
```

Nine widgets: total cost, tokens by model, cost by service, LLM p95 latency, vector
search latency, and cost over time. Every cost query reads `gen_ai.usage.cost_usd`, so
changing `pricing.yaml` changes the dashboard — no hardcoded rates.

---

## Development

```bash
go build -o bin/signoz-init ./cmd/signoz-init
go test ./internal/...                                   # unit tests
SIGNOZ_LIVE_TEST=1 go test -count=1 ./internal/verify/   # against a live stack
```

```
cmd/signoz-init/     CLI (init, dashboards)
internal/
  detect/            compose parsing + language/framework/AI-lib detection
  generate/          override synthesis, dep editing, embedded assets, dashboards
    files/           sitecustomize.py, otel-bootstrap.js, pricing.yaml, dashboards
  verify/            ClickHouse + API backends
  prompt/            detection table, LCS diff, verify report
demo/                the RAG stack (also the test fixture)
infra/               foundryctl casting.yaml
```

---

## Scope

**Supported today:** Docker Compose, Python (FastAPI/Flask/Django) and Node
(Express/Fastify/Nest), LLM providers and vector DBs via OpenLLMetry.

**Not in scope:** Kubernetes; the alert rule pack (SigNoz's rule schema is
undocumented). Cost governance is delivered through the dashboard.

---

## Beyond cost: RAG quality & session signals

Cost and latency tell you *that* something is wrong, not *what*. Two signals close
that gap — and they're ones auto-instrumentation fundamentally cannot produce,
because only the application knows them. `signoz-init` emits them as span
attributes that ride the same trace and roll up natively in SigNoz.

**Groundedness** (`gen_ai.response.groundedness`, 0–1) — how much of the model's
answer actually came from the retrieved context. A cheap, dependency-free lexical
heuristic (no second LLM call, no key, no added latency) that still catches the
failure that matters: an answer that wandered off the retrieved chunks. This is the
*"an agent hallucinates in production"* line from the Flying Blind brief, made
measurable. A grounded answer scores ~1.0; an off-context one scores ~0.

**Session rollups** (`session.id`, `gen_ai.conversation.id`) — so cost, latency, and
groundedness aggregate *per conversation*, not just per call. Turns "this request
cost $0.00004" into "this conversation cost $0.40" — the number a budget owner
actually cares about.

Both surface in the dashboard (avg groundedness, groundedness-over-time, cost by
conversation). See `demo/api/signoz_rag.py` for the ~60-line implementation.

## Roadmap

Deeper LLM-quality signals we'd add next, roughly in priority order:

- **LLM-as-judge evaluation** — faithfulness/relevance scoring via a grader model,
  for teams that want more than the lexical groundedness heuristic.
- **Retrieval quality** — chunk relevance scores, recall@k, and which retrieved
  chunks actually influenced the answer (retrieval-to-generation attribution).
- **Agent signals** — loop/retry detection, planning-vs-execution time split,
  tool-call success rate as a first-class metric, trajectory comparison across runs.
- **Session analytics** — repeated-question and abandonment rates as
  first-response-failure proxies (the `session.id` plumbing is already in place).
- **Cost optimization** — cache hit-rate, prompt-compression opportunities, and
  cheaper-model routing suggestions, on top of the spend tracking that exists today.
