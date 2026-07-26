"""RAG-quality and session signals for LLM observability.

These are the two things auto-instrumentation *cannot* produce, because only the
application knows them:

  - session/conversation id — so cost and latency roll up per conversation, not
    just per call ("this conversation cost $0.40").
  - groundedness — did the model's answer actually use the retrieved context, or
    did it wander off (a cheap, dependency-free hallucination signal for RAG).

Both are emitted as attributes on the current span, so they ride the same trace
as the LLM and vector-DB spans and are immediately group-by-able in SigNoz.

The tracer-provider promotion in signoz-init's bootstrap is what makes these
manual spans/attributes actually export — without it, get_tracer() would hand
back a no-op provider and these would silently vanish.
"""

import re
from typing import Iterable

try:
    from opentelemetry import trace
except Exception:  # noqa: BLE001 - app must run even without OTel present
    trace = None


# Tokens shorter than this are ignored when scoring overlap — "the", "is", "a"
# say nothing about whether an answer is grounded.
_MIN_TOKEN_LEN = 4
_WORD = re.compile(r"[a-z0-9]+")


def _tokens(text: str) -> set:
    return {t for t in _WORD.findall(text.lower()) if len(t) >= _MIN_TOKEN_LEN}


def groundedness(answer: str, sources: Iterable[str]) -> float:
    """Fraction of the answer's meaningful words that appear in the retrieved context.

    This is deliberately a cheap lexical heuristic, not an LLM judge: it needs no
    extra model call, no API key, and no latency, yet it still catches the failure
    that matters — an answer that talks about things the retrieval never returned.

    Returns 0.0–1.0. High = the answer stayed within the retrieved context;
    low = the model likely drew on parametric knowledge (a hallucination risk).
    """
    answer_tokens = _tokens(answer)
    if not answer_tokens:
        return 0.0

    source_tokens: set = set()
    for s in sources:
        source_tokens |= _tokens(s)

    grounded = answer_tokens & source_tokens
    return round(len(grounded) / len(answer_tokens), 4)


def annotate_rag(session_id: str, answer: str, sources: Iterable[str]) -> float:
    """Attach session + groundedness attributes to the active span.

    Returns the groundedness score (also useful to the caller). Never raises —
    telemetry must not break the request.
    """
    src = list(sources)
    score = groundedness(answer, src)
    if trace is None:
        return score
    try:
        span = trace.get_current_span()
        if span is not None:
            # gen_ai.* keeps these alongside the token/cost attributes SigNoz
            # already understands; session.id is the OTel-conventional name.
            span.set_attribute("session.id", session_id)
            span.set_attribute("gen_ai.conversation.id", session_id)
            span.set_attribute("gen_ai.response.groundedness", score)
            span.set_attribute("gen_ai.rag.source_count", len(src))
    except Exception:  # noqa: BLE001
        pass
    return score
