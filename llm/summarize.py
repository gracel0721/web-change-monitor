#!/usr/bin/env python3
"""Summarize a webpage change via a local Ollama model.

Contract (JSON on stdin -> JSON on stdout):

  stdin:  {"before": "<old normalized text>",
           "after":  "<new normalized text>",
           "diff":   "<human-readable diff>",
           "ollama_url":   "http://localhost:11434",
           "ollama_model": "llama3.2"}

  stdout: {"summary": "...", "importance": "LOW"|"MEDIUM"|"HIGH"}

Failures print {"error": "..."} on stdout and exit non-zero so the Go side can
keep the change with a null summary rather than crashing the worker.
"""

import json
import sys
import urllib.request
import urllib.error


SYSTEM_PROMPT = """\
You summarize webpage changes for a monitoring tool.

Given the BEFORE and AFTER text of a page and a diff, write a concise summary
of what changed (1-3 sentences, plain prose). Then assign an importance:

- HIGH: price, availability, security, outage, or notable business/announcement changes.
- MEDIUM: meaningful content/feature changes that are not critical.
- LOW: minor edits, formatting, typos, blog post churn.

Respond with STRICT JSON only, no prose, no markdown fences:
{"summary": "<...>", "importance": "LOW"|"MEDIUM"|"HIGH"}
"""


def build_prompt(before: str, after: str, diff: str) -> str:
    # Trim very large inputs so we stay within model context.
    def trim(s: str, n: int = 6000) -> str:
        return s if len(s) <= n else s[:n] + "\n[...truncated...]"
    return (
        f"BEFORE:\n{trim(before)}\n\n"
        f"AFTER:\n{trim(after)}\n\n"
        f"DIFF:\n{trim(diff)}\n\n"
        "Summarize the change and rate its importance as JSON."
    )


def call_ollama(url: str, model: str, prompt: str) -> str:
    endpoint = url.rstrip("/") + "/api/generate"
    payload = json.dumps({
        "model": model,
        "prompt": prompt,
        "system": SYSTEM_PROMPT,
        "stream": False,
        "format": "json",
    }).encode("utf-8")
    req = urllib.request.Request(
        endpoint, data=payload, headers={"Content-Type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.URLError as e:
        raise RuntimeError(f"cannot reach ollama at {endpoint}: {e}")
    # Ollama returns {"response": "<model text>"}
    return data.get("response", "")


def parse_model_output(text: str) -> dict:
    text = text.strip()
    # The model may wrap JSON in prose or fences despite instructions; recover.
    start = text.find("{")
    end = text.rfind("}")
    if start == -1 or end == -1 or end <= start:
        raise RuntimeError(f"no JSON object in model output: {text[:200]!r}")
    obj = json.loads(text[start:end + 1])
    importance = str(obj.get("importance", "LOW")).upper().strip()
    if importance not in ("LOW", "MEDIUM", "HIGH"):
        importance = "LOW"
    return {"summary": str(obj.get("summary", "")).strip(), "importance": importance}


def main() -> int:
    try:
        req = json.load(sys.stdin)
    except Exception as e:
        print(json.dumps({"error": f"invalid stdin json: {e}"}))
        return 2

    url = req.get("ollama_url") or "http://localhost:11434"
    model = req.get("ollama_model") or "llama3.2"
    before = req.get("before", "")
    after = req.get("after", "")
    diff = req.get("diff", "")

    try:
        raw = call_ollama(url, model, build_prompt(before, after, diff))
        result = parse_model_output(raw)
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        return 1

    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main())