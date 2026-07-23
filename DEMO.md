# Demo script — signoz-init

**Target length: 2:30–3:00.** Judges score Presentation Quality and UX directly, so
this is a scored artifact, not an afterthought. The whole demo is one idea:
*empty dashboard → one command → live AI trace with cost*, and everything else is
in service of making that landing hit.

---

## The core beat (memorise this)

Split screen. SigNoz dashboard on the right, empty. Terminal on the left. You type
**one command**. You refresh. A distributed trace appears — `web → api → qdrant →
LLM` — with token counts and a dollar cost on the LLM span. That's the demo. If you
only had 30 seconds, that beat alone would carry it.

Everything below is scaffolding to set up and pay off that one moment.

---

## Pre-flight (do BEFORE recording — not on camera)

Run this checklist so the recording is clean and reproducible:

```bash
# 1. SigNoz healthy and OTLP open
curl -sf http://localhost:8080/api/v1/health && echo "signoz ok"
curl -s -o /dev/null -w "otlp %{http_code}\n" -X POST http://localhost:4318/v1/traces \
  -H 'Content-Type: application/json' -d '{"resourceSpans":[]}'   # want 200

# 2. Fresh, UNinstrumented copy of the demo to run against on camera
rm -rf /tmp/rag-demo && cp -r demo /tmp/rag-demo
cd /tmp/rag-demo && rm -rf .signoz docker-compose.override.yml
# strip instrumentation deps so the tool has real work to show
python3 - <<'PY'
import json
r = open('api/requirements.txt').read().split('# Added by `signoz init`')[0].rstrip()+'\n'
open('api/requirements.txt','w').write(r)
d = json.load(open('web/package.json'))
d['dependencies'] = {k:v for k,v in d['dependencies'].items() if not k.startswith('@opentelemetry/')}
open('web/package.json','w').write(json.dumps(d, indent=2)+'\n')
PY

# 3. Give it a distinct project name + non-clashing ports so it won't collide
sed -i 's/^name: signoz-demo$/name: rag-demo/; s/"3000:3000"/"3300:3000"/; s/"8000:8000"/"8300:8000"/; s/"6333:6333"/"6633:6333"/' docker-compose.yml

# 4. Decide: real LLM or mock?
#    Real (Gemini):   cp <your .env> /tmp/rag-demo/.env
#    Mock (no key):   leave .env absent — demo still shows tokens + cost
```

Build the binary once: `go build -o bin/signoz-init ./cmd/signoz-init`.

**Have two terminals ready:** one in `/tmp/rag-demo`, one for sending traffic.

---

## The script

### 0:00 — The hook (spoken over a title card or the empty dashboard)

> "AI apps chain LLM calls, hit vector databases, rack up cost — and when something
> breaks in production, you're flying blind. Wiring up observability for all of it is
> a weekend of work. I made it one command."

Keep it to ~12 seconds. Don't explain the architecture yet — earn that.

### 0:15 — Show the app is genuinely blind

In terminal 2, start the uninstrumented stack and hit it:

```bash
docker compose up -d --build          # (can be pre-warmed; see note)
curl -s -X POST localhost:3300/ask -H 'Content-Type: application/json' \
  -d '{"q":"What is distributed tracing?"}' | jq
```

> "Here's a real RAG service — a Node gateway calling a FastAPI app that searches
> Qdrant and calls an LLM. It works. But in SigNoz…" *(cut to empty dashboard)*
> "…nothing. No traces. We can't see any of it."

**Beat: the empty dashboard.** Let it sit for a second. That emptiness is the problem
you're about to solve.

### 0:45 — The one command

Terminal 1, in the project:

```bash
signoz-init init .
```

Talk over the detection table as it appears:

> "It reads my compose file, figures out what each service is — FastAPI here, Express
> there, Qdrant as a vector store — and finds the AI libraries. It didn't guess; if it
> wasn't sure, it'd ask."

### 1:00 — The safety beat (this is what makes it a *product*)

The diff appears. Slow down here.

> "Before it writes anything, it shows me the exact diff. And notice —" *(scroll to
> the top)* "— it never touches my docker-compose.yml. Everything goes into an
> override file. Undoing all of this is a single `rm`."

Press **y**.

> "It adds the instrumentation with zero changes to my application code."

### 1:20 — The payoff

Restart and send traffic:

```bash
docker compose up -d --build
curl -s -X POST localhost:3300/ask -H 'Content-Type: application/json' \
  -d '{"q":"How does cost tracking work?"}' > /dev/null
```

Then let the **verify phase** run (or show it if init did it inline):

> "And it doesn't just write config and hope. It polls SigNoz and tells me what
> actually arrived."

```
✓ 2 services reporting, 168 spans
  7 LLM spans with token counts
  7 spans with cost attribution
```

### 1:45 — The money shot

Refresh SigNoz. Open the trace.

> "There it is. One request, one trace, across all four services — the gateway, the
> API, the vector search, and the LLM call."

Click into the LLM span. Point at the attributes.

> "And on the LLM span: input tokens, output tokens, and **cost in actual dollars**.
> SigNoz tracks tokens — but it doesn't compute cost. We do, from a pricing table you
> can edit."

**This is the peak. Hold it.** The distributed trace + the dollar figure is the whole
pitch made visible.

### 2:15 — The dashboard (optional, if time)

```bash
signoz-init dashboards --api-key <key>
```

> "One more command installs a cost dashboard — spend over time, cost by model, cost
> by service. Observability *and* governance."

### 2:30 — Close

> "Observability shouldn't be a weekend. It should be a `git diff`. That's signoz-init."

*(Optional: `rm docker-compose.override.yml && docker compose up -d` — show it revert
clean. Powerful if you have the seconds.)*

---

## Recording notes

- **Pre-warm the Docker builds.** `docker compose build` before recording so the
  on-camera `up -d --build` is seconds, not minutes. Nobody wants to watch pip install.
  If builds are still slow, cut away or speed-ramp that clip.
- **Font size up**, terminal and browser. Judges may watch on a laptop.
- **Mock vs real LLM:** mock is safer (no network flake, no key on screen) and still
  shows tokens + cost. Use real only if you want the authenticity and have rehearsed
  it. Either way, **never show the key** — `.env` is loaded off-camera.
- **The verify step is a differentiator** — most "instrument my app" demos stop at
  "config written." Ours proves telemetry landed. Don't rush past it.
- **If a request 502s once** on first boot, that's the startup race — just re-run it.
  The healthcheck fix should prevent it, but have a second curl ready.
- Keep cuts tight. Dead air on a `docker build` is where attention dies.

---

## Fallback if live demo is risky

Record the working run once, clean, and narrate over the capture. A judged demo video
does not have to be live — it has to be *clear*. A pre-recorded, well-narrated run
beats a live one that stalls on a cold build.

---

## The one-sentence version (for the submission blurb)

> `signoz-init` turns full OpenTelemetry instrumentation — HTTP, DB, and LLM spans
> with token counts and per-request dollar cost — into a single command against any
> Docker Compose stack, without touching your compose file or application code.
