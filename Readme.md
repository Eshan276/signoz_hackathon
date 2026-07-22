Gaps and improvements worth adding

1. Quality/output evaluation layer

No built-in LLM-as-judge scoring, hallucination detection, or groundedness/faithfulness checks (RAG answer vs retrieved context)
No semantic similarity or regression testing between prompt versions
Langfuse/LangSmith/Braintrust all have this natively; SigNoz relies on third-party integration

2. RAG-specific observability

Vector DB latency is tracked, but not retrieval quality: chunk relevance scores, recall@k, context precision/recall, embedding drift over time
No visibility into re-ranking steps or retrieval-to-generation attribution (which chunks actually influenced the final answer)

4. Agent-specific signals

Waterfall tracing exists, but no dedicated metrics for: loop/retry detection thresholds, planning-vs-execution time split, tool-call success/failure rate as a first-class metric, or multi-agent handoff failure tracking
No agent "trajectory" comparison across runs (did it take the same path this time vs last time for the same task)

5. User-level / session-level analytics

Token/cost tracking is per model/operation/user, but no conversation-level satisfaction proxies: thumbs up/down capture, session abandonment rate, repeated-question rate (signal that the bot failed the first time)

6. Cost optimization beyond tracking

Tracks spend but doesn't seem to suggest optimization (e.g., prompt compression opportunities, caching hit-rate dashboards, routing suggestions to cheaper models for simple queries)
