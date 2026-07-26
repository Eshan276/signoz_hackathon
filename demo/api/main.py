"""RAG service: embed the query, search Qdrant, ask an LLM.

Deliberately contains no OpenTelemetry code. `signoz init` is what wires up
instrumentation; if this file needs editing to get traces, the premise is broken.
"""

import os
import time

from fastapi import FastAPI
from pydantic import BaseModel
from qdrant_client import QdrantClient
from qdrant_client.models import Distance, PointStruct, VectorParams

from llm import LLMClient
from signoz_rag import annotate_rag, begin_session

QDRANT_URL = os.getenv("QDRANT_URL", "http://qdrant:6333")
COLLECTION = "docs"
VECTOR_SIZE = 384

app = FastAPI(title="signoz-init demo RAG API")
llm = LLMClient()

# Built lazily rather than at import time. Instrumentation patches QdrantClient
# methods during startup; a client constructed at import can capture unpatched
# bound methods and silently produce no spans.
_qdrant: QdrantClient | None = None


def get_qdrant() -> QdrantClient:
    global _qdrant
    if _qdrant is None:
        _qdrant = QdrantClient(url=QDRANT_URL)
    return _qdrant


class AskRequest(BaseModel):
    q: str
    # Optional: ties this call to a conversation so cost/latency/groundedness
    # roll up per session, not just per request. Defaults to "anonymous".
    session_id: str = "anonymous"


class AskResponse(BaseModel):
    answer: str
    sources: list[str]
    model: str
    usage: dict
    groundedness: float


# A tiny corpus so the demo is self-contained and deterministic.
CORPUS = [
    "SigNoz is an open-source observability platform built natively on OpenTelemetry.",
    "OpenTelemetry provides vendor-neutral APIs for traces, metrics, and logs.",
    "Qdrant is a vector database used for semantic search in RAG pipelines.",
    "Distributed tracing follows a single request across multiple services.",
    "LLM observability tracks token usage, latency, and cost per model call.",
    "A span is a single unit of work; spans form a trace via parent-child links.",
    "The gen_ai semantic conventions standardize how LLM calls are recorded.",
    "Cost attribution multiplies token counts by per-model pricing rates.",
]


def embed(text: str) -> list[float]:
    """Deterministic hash embedding.

    A real model would be better, but this keeps the demo dependency-light and
    reproducible. The span it produces is what matters for the trace shape.
    """
    import hashlib

    vec = [0.0] * VECTOR_SIZE
    for token in text.lower().split():
        h = int(hashlib.md5(token.encode()).hexdigest(), 16)
        vec[h % VECTOR_SIZE] += 1.0
    norm = sum(v * v for v in vec) ** 0.5
    return [v / norm for v in vec] if norm else vec


@app.on_event("startup")
def seed() -> None:
    for attempt in range(30):
        try:
            client = get_qdrant()
            existing = {c.name for c in client.get_collections().collections}
            if COLLECTION not in existing:
                client.create_collection(
                    collection_name=COLLECTION,
                    vectors_config=VectorParams(size=VECTOR_SIZE, distance=Distance.COSINE),
                )
                client.upsert(
                    collection_name=COLLECTION,
                    points=[
                        PointStruct(id=i, vector=embed(doc), payload={"text": doc})
                        for i, doc in enumerate(CORPUS)
                    ],
                )
            return
        except Exception:
            # Qdrant may still be starting; back off and retry.
            time.sleep(1)
    raise RuntimeError(f"could not reach Qdrant at {QDRANT_URL}")


@app.get("/health")
def health() -> dict:
    return {"status": "ok"}


# Sync `def` on purpose. Starlette runs sync endpoints via anyio's
# to_thread.run_sync, which *does* copy contextvars into the worker thread, so the
# OTel span context follows and downstream Qdrant/LLM spans stay attached to the
# request. Wrapping blocking calls in run_in_threadpool from an async endpoint
# does NOT propagate context and produces orphaned root spans.
# Verified both ways 2026-07-22.
@app.post("/ask", response_model=AskResponse)
def ask(req: AskRequest) -> AskResponse:
    # Tag the whole request (including the LLM span) with the session, before any
    # child spans are created, so cost/latency/groundedness roll up per session.
    begin_session(req.session_id)

    # Uses the older search() rather than query_points(): OpenLLMetry's Qdrant
    # instrumentor wraps search/query but not query_points, so the newer API
    # produces no vector-DB span. Verified against
    # opentelemetry-instrumentation-qdrant 0.38.7 on 2026-07-22.
    hits = get_qdrant().search(
        collection_name=COLLECTION,
        query_vector=embed(req.q),
        limit=3,
    )
    sources = [h.payload["text"] for h in hits]

    context = "\n".join(f"- {s}" for s in sources)
    prompt = (
        "Answer the question using only the context below.\n\n"
        f"Context:\n{context}\n\nQuestion: {req.q}"
    )

    completion = llm.complete(prompt)

    # Attach groundedness to the request span — the RAG-quality signal
    # auto-instrumentation can't produce: did the answer stay within the chunks we
    # retrieved, or wander off (a cheap hallucination proxy)?
    score = annotate_rag(completion.text, sources)

    return AskResponse(
        answer=completion.text,
        sources=sources,
        model=completion.model,
        usage=completion.usage,
        groundedness=score,
    )
