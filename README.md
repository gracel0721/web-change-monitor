# webwatch

`webwatch` is a command-line tool that monitors web pages for changes, stores
snapshots in SQLite, and summarizes detected changes with a local LLM
(Ollama). It's a small **polyglot** system: Go owns fetching, scheduling, the
database, and the CLI; Python owns LLM summarization.

```
Scheduler (Go) → fetch → normalize → compare → [Python → Ollama] → store + notify
```

## Install

```sh
go build -o webwatch ./cmd/webwatch
```

The binary is self-contained (pure-Go SQLite via `modernc.org/sqlite`, no CGO).

## Configure the LLM

`webwatch` calls a local Ollama model for change summaries. Make sure Ollama is
running and a chat model is pulled, then point `webwatch` at it:

```sh
ollama pull qwen2.5:7b
webwatch config ollama_model qwen2.5:7b     # default is llama3.2
webwatch config                              # show current settings
```

Without Ollama (or if a summarization call fails) changes are still recorded —
the LLM step is non-fatal and the summary is simply left empty.

## Usage

```sh
webwatch add https://example.com/pricing --name "Pricing" --freq 1h
webwatch list
webwatch run            # one check pass (all due sites)
webwatch run --id 1     # force a check on one site
webwatch start          # run the scheduler daemon (Ctrl-C to stop)
webwatch changes        # show detected changes
webwatch changes --id 1
webwatch remove 1
webwatch config webhook_url https://hooks.example.com/webwatch   # optional
```

`--db <path>` overrides the default database location (`~/.webwatch/webwatch.db`).

### What `list` looks like

```
ID  URL                       LAST CHECK   STATUS
1   example.com/pricing      2 hours ago  changed
2   company.com/careers       1 hour ago  unchanged
```

### What a change looks like

```
🚨 CHANGE DETECTED

https://example.com/pricing

Before:
  Pro — $49/month

After:
  Pro — $59/month

AI SUMMARY:
The Pro plan increased by $10/month. No other pricing changes detected.

Importance: HIGH
```

## How it works

1. **Fetch** (`net/http`, 30s timeout, redirect-safe) retrieves raw HTML.
2. **Normalize** strips `<script>`/`<style>`/`<nav>`/boilerplate and collapses
   whitespace into stable text.
3. **Compare** takes the sha256 of the normalized text; if it matches the last
   snapshot, nothing changed. Otherwise a line diff is produced.
4. **Summarize**: the Go worker spawns `llm/summarize.py` with a JSON contract
   on stdin (`before`/`after`/`diff`); the script calls Ollama and returns
   `{summary, importance}` JSON on stdout. That process boundary is the
   polyglot seam — it can be swapped for an HTTP microservice later without
   touching Go.
5. **Persist + notify**: a new snapshot and a change row are written to
   SQLite, the `🚨` block is printed, and (if configured) a webhook receives
   the event.

## Database schema

```
websites:  id, url (unique), name, created_at, check_frequency_seconds,
           last_checked_at, next_check_at, last_status, last_error
snapshots: id, website_id, content_hash, content, created_at
changes:   id, website_id, old_snapshot_id, new_snapshot_id, diff,
           summary, importance, created_at
```

## Project layout

```
cmd/webwatch/main.go         entrypoint
internal/
  cli/                       cobra commands + signal handling
  config/                    ~/.webwatch paths + settings
  db/                        SQLite open/migrate/queries
  fetcher/                   HTTP fetching
  normalize/                 HTML → stable text
  compare/                   hashing + diffing
  worker/                    the check pipeline
  scheduler/                 tick loop daemon
  llmclient/                 spawns the Python summarizer
  notify/                    🚨 block + webhook
llm/summarize.py             Ollama summarizer (stdlib only)
```

## Out of scope (for now)

- JS-rendered pages (no headless browser) — client-rendered sites won't diff
  meaningfully with plain HTTP fetching.
- Email / desktop notifications.
- Multi-user.

## License

See `LICENSE`.