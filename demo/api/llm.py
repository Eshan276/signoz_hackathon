"""LLM client with a real provider path and a keyless mock fallback.

The mock is not decoration. It means judges can reproduce the demo without their
own API key, and it lets us exercise the whole telemetry pipeline (including cost
math) without burning credits. Both paths must produce usable token counts.

The real path uses the OpenAI SDK, which works against any OpenAI-compatible
endpoint. Setting LLM_BASE_URL points it elsewhere — e.g. Gemini's compatibility
endpoint — which is how the demo proves the instrumentation is not tied to one
vendor. Because it is still the OpenAI SDK on the wire, OpenLLMetry's OpenAI
instrumentor produces the spans either way.
"""

import os
import random
import time
from dataclasses import dataclass

MODEL = os.getenv("LLM_MODEL", "gpt-4o-mini")
# Optional OpenAI-compatible base URL. Empty means api.openai.com.
BASE_URL = os.getenv("LLM_BASE_URL", "").strip()


@dataclass
class Completion:
    text: str
    model: str
    usage: dict


class LLMClient:
    def __init__(self) -> None:
        self.api_key = os.getenv("OPENAI_API_KEY", "").strip()
        self.mock = not self.api_key
        self._client = None
        if not self.mock:
            from openai import OpenAI

            kwargs = {"api_key": self.api_key}
            if BASE_URL:
                kwargs["base_url"] = BASE_URL
            self._client = OpenAI(**kwargs)

    def complete(self, prompt: str) -> Completion:
        if self.mock:
            return self._complete_mock(prompt)
        return self._complete_openai(prompt)

    def _complete_openai(self, prompt: str) -> Completion:
        resp = self._client.chat.completions.create(
            model=MODEL,
            messages=[{"role": "user", "content": prompt}],
            max_tokens=200,
        )
        return Completion(
            text=resp.choices[0].message.content,
            model=resp.model,
            usage={
                "input_tokens": resp.usage.prompt_tokens,
                "output_tokens": resp.usage.completion_tokens,
                "total_tokens": resp.usage.total_tokens,
            },
        )

    def _complete_mock(self, prompt: str) -> Completion:
        """Fake a completion with realistic latency and token counts.

        Emits a span following the OpenTelemetry GenAI semantic conventions, the
        same shape the real OpenAI instrumentor produces. Without this the mock
        path yields no gen_ai.* attributes at all, so cost attribution and the
        LLM dashboards stay empty and the demo cannot be run without an API key.

        Roughly 4 characters per token matches typical BPE ratios closely enough
        that cost figures look believable.
        """
        question = prompt.rsplit("Question:", 1)[-1].strip()
        text = (
            f"Based on the retrieved context, here is what I can tell you about "
            f"'{question}': the relevant documents describe the core concepts and "
            f"how they relate to observability practice."
        )

        input_tokens = max(1, len(prompt) // 4)
        output_tokens = max(1, len(text) // 4)

        try:
            from opentelemetry import trace

            tracer = trace.get_tracer("signoz-init.demo.llm")
            # Span name follows the convention "{operation} {model}".
            with tracer.start_as_current_span(f"chat {MODEL}") as span:
                span.set_attribute("gen_ai.system", "openai")
                span.set_attribute("gen_ai.operation.name", "chat")
                span.set_attribute("gen_ai.request.model", MODEL)
                span.set_attribute("gen_ai.response.model", MODEL)
                span.set_attribute("gen_ai.usage.input_tokens", input_tokens)
                span.set_attribute("gen_ai.usage.output_tokens", output_tokens)
                span.set_attribute("signoz_init.mock", True)
                time.sleep(random.uniform(0.3, 0.9))
        except Exception:  # noqa: BLE001 - telemetry must never break the app
            time.sleep(random.uniform(0.3, 0.9))

        return Completion(
            text=text,
            model=MODEL,
            usage={
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "total_tokens": input_tokens + output_tokens,
            },
        )
