# Navigara CLI

Backfill local Claude Code and Codex sessions into Navigara. The CLI rebuilds the
OpenTelemetry the tools would have sent live, so a backfilled session looks the same in
Navigara as a live one.

## Set up this computer

```sh
navigara setup
```

On macOS and Linux, the wizard selects Claude Code/Codex, asks for your Navigara
API base URL and analytics ingestor token, confirms contributor email and history
window, and previews the import before applying changes. Token input is hidden
on terminals. Close running tool sessions before taking the preview.

Setup preserves unrelated settings and saves private backups beside changed files.
Claude uses user settings; Codex uses its configuration plus a bash/zsh identity
block. Existing conflicting or managed telemetry settings require manual resolution.
Restart your tools in a new terminal afterward; launch desktop/IDE tools from that
terminal to inherit identity. Setup reports configuration, not observed live traffic.

The receiver must advertise session backfill deduplication. Live data wins over
backfill for the same session; partial live sessions are not filled in automatically.
Uploads reuse the exact preview files. If interrupted, retry the printed directory:

```sh
navigara setup --resume '/path/to/saved/payload'
```

Resume asks for the token again and verifies saved file hashes before uploading.
The token is stored in private tool settings for ongoing telemetry, not in the
saved upload manifest.

```sh
navigara sessions upload --last 30d                 # write OTLP request bodies, send nothing
navigara sessions upload --last 30d --send \
  --url https://app.navigara.com --token "$NAVIGARA_TOKEN"
```

The token is an API key with the analytics ingestor role. `NAVIGARA_TOKEN` is the default.

## What it sends

Per session, the same records the tools send live:

| Claude Code | Codex |
| -- | -- |
| `user_prompt`, `api_request`, `tool_decision`, `tool_result`, `skill_activated`, `subagent_completed`, Stop hooks | `codex.conversation_starts`, `codex.user_prompt`, `codex.tool_result` (with `mcp_server`) |
| `claude_code.cost.usage`, `claude_code.token.usage` | `codex.turn.token_usage` |

Every record carries the session id, the signed-in email and the repository URL. Requests
carry the resource attribute `navigara.backfill=true`. Prompt text, tool input and output are
never sent.

* **Identity**: the email, `user.id` and account ids from `~/.claude.json`, and the email of the
  Codex sign-in. `--email` overrides both. Without an email the CLI refuses to export, unless
  `--allow-anonymous` is set.
* **Cost**: Claude Code writes its own per-model totals (`cost-state`) when a session ends; the
  CLI matches them exactly, background calls included. A session without them (still running,
  or cut by `--until`) is priced from a table and flagged `cost=estimated`. Codex is priced by
  Navigara from the tokens.
* **Subagents and teammates**: their transcripts (`<session>/subagents/`) count in the parent
  session, like live. Codex child threads are sessions of their own, like live.
* **Repository**: Claude Code transcripts record only the working directory; the CLI reads that
  directory's `origin` remote. Credentials are stripped.

## Flags

| Flag | Meaning |
| -- | -- |
| `--source all\|claude\|codex` | Which tool to read. |
| `--last 30d`, `--since`, `--until` | Time window, by duration or RFC 3339 time. |
| `--session ID` | Only this session; repeat for more. Useful to compare one session with live data. |
| `--output DIR` | New directory for the request bodies (`logs-000001.json`, `metrics-000001.json`, …). |
| `--send`, `--url`, `--token` | Post the bodies, logs first. Retries throttling and server errors. |
| `--claude-home`, `--codex-home` | Tool homes, default `~/.claude` and `~/.codex`. |

Sending the same backfill twice changes nothing: Navigara drops identical records.

## Limits

* Claude Code deletes transcripts after its cleanup period (30 days by default).
* A slash command typed last, such as `/exit`, never reaches the transcript.
* Claude Code background calls without a transcript row (`prompt_suggestion`, `agent_summary`)
  count in cost and tokens at the session end, not when they happened.
* One-shot `codex exec` runs that keep no rollout file cannot be backfilled.
