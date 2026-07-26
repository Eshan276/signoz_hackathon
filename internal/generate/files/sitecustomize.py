"""Zero-touch OpenTelemetry bootstrap for Python services.

Python imports `sitecustomize` automatically at interpreter startup, before any
application code runs. Mounting this file into a container and prepending its
directory to PYTHONPATH gets instrumentation installed without touching the app —
the same mechanism the OpenTelemetry Operator uses for Kubernetes auto-injection.

This is the most fragile file in the repo. Every failure mode here must be
non-fatal: a broken bootstrap must never take down the user's application. If
instrumentation cannot start, we log to stderr and let the app run uninstrumented.

Two jobs:
  1. Install OTel + OpenLLMetry instrumentation.
  2. Attach cost (in USD) to LLM spans by multiplying token counts by a pricing table.
"""

import os
import sys

_PREFIX = "[signoz-init]"


def _log(msg: str) -> None:
    print(f"{_PREFIX} {msg}", file=sys.stderr)


def _load_pricing() -> dict:
    """Load model -> pricing from the mounted pricing.yaml.

    Hand-rolled parser rather than PyYAML: this runs inside the *user's* image,
    where we cannot assume any third-party package is installed. The format is
    fixed and simple, so a real YAML parser would be over-engineering.
    """
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "pricing.yaml")
    pricing: dict = {}
    try:
        with open(path) as fh:
            current = None
            for raw in fh:
                line = raw.rstrip()
                if not line.strip() or line.strip().startswith("#"):
                    continue
                if not line.startswith(" ") and line.rstrip().endswith(":"):
                    current = line.rstrip()[:-1].strip().strip('"').strip("'")
                    pricing[current] = {}
                elif current and ":" in line:
                    key, _, val = line.strip().partition(":")
                    try:
                        pricing[current][key.strip()] = float(val.strip())
                    except ValueError:
                        pass
    except FileNotFoundError:
        _log(f"pricing.yaml not found at {path}; cost attribution disabled")
    except Exception as exc:  # noqa: BLE001 - never fail the app
        _log(f"could not parse pricing.yaml ({exc}); cost attribution disabled")
    return pricing


def _resolve_price(model: str, pricing: dict):
    """Match a model name against the pricing table.

    Providers return versioned names ("gpt-4o-mini-2024-07-18") and our mock appends
    a suffix, so exact lookup is not enough. Fall back to the longest key that is a
    substring of the model name — longest wins so "gpt-4o-mini" beats "gpt-4o".
    """
    if not model:
        return None
    if model in pricing:
        return pricing[model]
    best = None
    for key, rates in pricing.items():
        if key in model and (best is None or len(key) > len(best[0])):
            best = (key, rates)
    return best[1] if best else None


def _compute_cost(attrs: dict, pricing: dict):
    """Return cost in USD for an LLM span, or None if it is not one."""
    model = (
        attrs.get("gen_ai.response.model")
        or attrs.get("gen_ai.request.model")
        or attrs.get("llm.request.model")
        or attrs.get("llm.response.model")
    )
    if not model:
        return None

    inp = attrs.get("gen_ai.usage.input_tokens")
    if inp is None:
        inp = attrs.get("gen_ai.usage.prompt_tokens") or attrs.get("llm.usage.prompt_tokens")
    out = attrs.get("gen_ai.usage.output_tokens")
    if out is None:
        out = attrs.get("gen_ai.usage.completion_tokens") or attrs.get(
            "llm.usage.completion_tokens"
        )
    if inp is None and out is None:
        return None

    rates = _resolve_price(str(model), pricing)
    if not rates:
        return None

    # Pricing is expressed per 1M tokens.
    cost = (float(inp or 0) / 1_000_000.0) * rates.get("input", 0.0)
    cost += (float(out or 0) / 1_000_000.0) * rates.get("output", 0.0)
    return round(cost, 8)


def _install_cost_processor(pricing: dict) -> None:
    """Attach gen_ai.usage.cost_usd to LLM spans by wrapping the exporter.

    Mutating a span in SpanProcessor.on_end does not work: by then the span is a
    ReadableSpan and the exporter has already captured its attributes, so the new
    value never leaves the process (verified 2026-07-22 — cost always exported 0).

    Wrapping the exporter instead lets us rewrite attributes on the ReadableSpan
    just before serialisation, which is the last point where a mutation is visible.
    """
    from opentelemetry import trace
    from opentelemetry.sdk.trace.export import SpanExporter

    provider = trace.get_tracer_provider()
    processors = getattr(
        getattr(provider, "_active_span_processor", None), "_span_processors", ()
    )
    if not processors:
        _log("no active span processors; cost attribution skipped")
        return

    class CostExporter(SpanExporter):
        def __init__(self, inner):
            self._inner = inner

        def export(self, spans):
            for span in spans:
                try:
                    src = span.attributes or {}
                    cost = _compute_cost(src, pricing)
                    if cost is None:
                        continue

                    attrs = span._attributes

                    def put(key, value):
                        # ReadableSpan exposes the attribute mapping; BoundedAttributes
                        # keeps its data in _dict.
                        if hasattr(attrs, "_dict"):
                            attrs._dict[key] = value
                        elif isinstance(attrs, dict):
                            attrs[key] = value

                    put("gen_ai.usage.cost_usd", cost)

                    # Normalise token names. The real OpenAI instrumentor emits the
                    # older prompt_tokens/completion_tokens; copy them to the
                    # canonical input_tokens/output_tokens so dashboards and verify
                    # see one consistent name regardless of provider.
                    if "gen_ai.usage.input_tokens" not in src:
                        inp = src.get("gen_ai.usage.prompt_tokens")
                        if inp is not None:
                            put("gen_ai.usage.input_tokens", inp)
                    if "gen_ai.usage.output_tokens" not in src:
                        out = src.get("gen_ai.usage.completion_tokens")
                        if out is not None:
                            put("gen_ai.usage.output_tokens", out)
                except Exception:  # noqa: BLE001 - never break export
                    pass
            return self._inner.export(spans)

        def shutdown(self):
            return self._inner.shutdown()

        def force_flush(self, timeout_millis: int = 30000) -> bool:
            return self._inner.force_flush(timeout_millis)

    wrapped = 0
    for proc in processors:
        inner = getattr(proc, "span_exporter", None)
        if inner is None or isinstance(inner, CostExporter):
            continue
        # span_exporter is a read-only property on BatchSpanProcessor backed by
        # _batch_processor._exporter, so assign through the backing field first.
        batch = getattr(proc, "_batch_processor", None)
        if batch is not None and hasattr(batch, "_exporter"):
            batch._exporter = CostExporter(inner)
            wrapped += 1
            continue
        try:
            proc.span_exporter = CostExporter(inner)
            wrapped += 1
        except AttributeError:
            pass

    if wrapped:
        _log(f"cost attribution enabled ({wrapped} exporter(s))")
    else:
        _log("could not wrap exporter; cost attribution skipped")


def _promote_global_tracer_provider() -> None:
    """Make Traceloop's tracer provider the global one.

    Traceloop keeps its provider internal and never calls trace.set_tracer_provider,
    so the global stays a ProxyTracerProvider. Any manual span the user writes with
    trace.get_tracer(...) is then a NonRecordingSpan that silently vanishes — their
    instrumented libraries produce spans while their own code produces nothing, with
    no error to explain it. Verified 2026-07-23.

    Promoting Traceloop's provider means hand-written spans join the same trace and
    export through the same pipeline (including our cost processor).
    """
    try:
        from opentelemetry import trace
        from opentelemetry.sdk.trace import TracerProvider

        current = trace.get_tracer_provider()
        if isinstance(current, TracerProvider):
            return  # already a real SDK provider

        from traceloop.sdk.tracing.tracing import TracerWrapper

        wrapper = TracerWrapper()
        provider = getattr(wrapper, "_TracerWrapper__tracer_provider", None)
        if provider is None:
            provider = getattr(wrapper, "_tracer_provider", None)
        if provider is None:
            _log("could not locate Traceloop's tracer provider; manual spans will not export")
            return

        # The global provider can only be set once, and the SDK logs a warning if
        # something got there first.
        trace._TRACER_PROVIDER = None
        trace.set_tracer_provider(provider)
        _log("global tracer provider set (manual spans will export)")
    except Exception as exc:  # noqa: BLE001 - never fail the app
        _log(f"could not promote tracer provider ({exc}); manual spans may not export")


def _install_http_instrumentation() -> None:
    """Install web-framework instrumentation that OpenLLMetry does not provide.

    Traceloop already instruments LLM providers, vector DBs, and the HTTP *clients*
    (requests, urllib3). It does NOT instrument web frameworks, so without this
    there is no root server span and every LLM/vector span is orphaned.

    Only add what is missing. Re-instrumenting something Traceloop already patched
    logs "Attempting to instrument while already instrumented" and can break the
    existing patch — this silently killed Qdrant spans until it was found.
    """
    installed = []

    for module_path, cls, label in (
        ("opentelemetry.instrumentation.fastapi", "FastAPIInstrumentor", "fastapi"),
        ("opentelemetry.instrumentation.flask", "FlaskInstrumentor", "flask"),
        ("opentelemetry.instrumentation.django", "DjangoInstrumentor", "django"),
    ):
        try:
            mod = __import__(module_path, fromlist=[cls])
            getattr(mod, cls)().instrument()
            installed.append(label)
        except Exception:  # noqa: BLE001 - framework absent or already patched
            pass

    if installed:
        _log(f"web framework instrumentation: {', '.join(installed)}")
    else:
        _log(
            "no web framework instrumentation installed; LLM spans may appear "
            "without a parent request span"
        )


def _install_ai_instrumentation() -> None:
    """Explicitly instrument LLM providers and vector DBs.

    Traceloop.init() is supposed to do this, but its per-library init functions
    swallow every exception and return silently. On this stack the Qdrant
    instrumentor never activated, producing a RAG trace with no vector-DB span
    and no error anywhere. Calling the instrumentors directly is both more
    reliable and more debuggable.

    Each instrumentor guards on is_instrumented_by_opentelemetry so this is safe
    even when Traceloop did succeed.
    """
    installed = []

    for module_path, cls, label in (
        ("opentelemetry.instrumentation.qdrant", "QdrantInstrumentor", "qdrant"),
        ("opentelemetry.instrumentation.chromadb", "ChromaInstrumentor", "chromadb"),
        ("opentelemetry.instrumentation.pinecone", "PineconeInstrumentor", "pinecone"),
        ("opentelemetry.instrumentation.weaviate", "WeaviateInstrumentor", "weaviate"),
        ("opentelemetry.instrumentation.milvus", "MilvusInstrumentor", "milvus"),
        ("opentelemetry.instrumentation.openai", "OpenAIInstrumentor", "openai"),
        ("opentelemetry.instrumentation.anthropic", "AnthropicInstrumentor", "anthropic"),
        ("opentelemetry.instrumentation.langchain", "LangchainInstrumentor", "langchain"),
        ("opentelemetry.instrumentation.llamaindex", "LlamaIndexInstrumentor", "llamaindex"),
        ("opentelemetry.instrumentation.bedrock", "BedrockInstrumentor", "bedrock"),
        ("opentelemetry.instrumentation.cohere", "CohereInstrumentor", "cohere"),
        ("opentelemetry.instrumentation.ollama", "OllamaInstrumentor", "ollama"),
    ):
        try:
            mod = __import__(module_path, fromlist=[cls])
            inst = getattr(mod, cls)()
            if not inst.is_instrumented_by_opentelemetry:
                inst.instrument()
                installed.append(label)
        except Exception:  # noqa: BLE001 - library not present in this image
            pass

    if installed:
        _log(f"ai instrumentation: {', '.join(installed)}")


# The current session id, set per-request by the app via signoz_init.set_session().
# A contextvar so it is correct under async and threadpool concurrency.
import contextvars  # noqa: E402

_current_session: "contextvars.ContextVar[str]" = contextvars.ContextVar(
    "signoz_init_session", default=""
)


def set_session(session_id: str) -> None:
    """Tag every span in the current request with a session/conversation id.

    Call this once at the start of a request. Because cost lives on the LLM span
    and the request lives on the server span — different spans — stamping the id
    onto *all* spans via a processor is what lets SigNoz roll cost, latency, and
    groundedness up per conversation. Without this, a "cost by session" query
    finds no single span carrying both cost and session.
    """
    if session_id:
        _current_session.set(str(session_id))


def _install_session_processor() -> None:
    """Stamp session.id onto every span at start, from the request contextvar."""
    try:
        from opentelemetry import trace
        from opentelemetry.sdk.trace import SpanProcessor

        outer = _current_session

        class SessionProcessor(SpanProcessor):
            def on_start(self, span, parent_context=None):
                sid = outer.get()
                if sid:
                    span.set_attribute("session.id", sid)
                    span.set_attribute("gen_ai.conversation.id", sid)

            def on_end(self, span):
                pass

            def shutdown(self):
                pass

            def force_flush(self, timeout_millis: int = 30000) -> bool:
                return True

        provider = trace.get_tracer_provider()
        if hasattr(provider, "add_span_processor"):
            provider.add_span_processor(SessionProcessor())
            _log("session attribution enabled")
    except Exception as exc:  # noqa: BLE001
        _log(f"session processor unavailable ({exc})")


def _bootstrap() -> None:
    if os.environ.get("SIGNOZ_INIT_DISABLE"):
        return

    # Guard against double-init: uvicorn --reload and gunicorn fork children each
    # re-enter the interpreter, and initialising twice produces duplicate spans.
    if os.environ.get("_SIGNOZ_INIT_ACTIVE"):
        return
    os.environ["_SIGNOZ_INIT_ACTIVE"] = "1"

    service = os.environ.get("OTEL_SERVICE_NAME", "unknown-service")

    try:
        from traceloop.sdk import Traceloop
    except ImportError:
        _log(
            "traceloop-sdk not installed in this image; skipping instrumentation.\n"
            f"{_PREFIX} add 'traceloop-sdk' to requirements and rebuild, or re-run "
            "`signoz init` to have it added for you."
        )
        return

    endpoint = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")

    # Traceloop resolves its exporter target from TRACELOOP_BASE_URL and ignores
    # the api_endpoint kwarg in the OTLP path, so set the env var explicitly.
    # Verified 2026-07-22: passing api_endpoint alone silently exports nowhere.
    os.environ.setdefault("TRACELOOP_BASE_URL", endpoint)

    try:
        Traceloop.init(
            app_name=service,
            api_endpoint=endpoint,
            disable_batch=False,
            telemetry_enabled=False,  # no phone-home from Traceloop itself
        )
        _log(f"instrumented '{service}' -> {endpoint}")
    except Exception as exc:  # noqa: BLE001
        _log(f"Traceloop.init failed ({exc}); app continues uninstrumented")
        return

    _promote_global_tracer_provider()
    _install_http_instrumentation()
    _install_ai_instrumentation()
    _install_session_processor()

    pricing = _load_pricing()
    if pricing:
        try:
            _install_cost_processor(pricing)
        except Exception as exc:  # noqa: BLE001
            _log(f"cost processor unavailable ({exc})")


_bootstrap()
