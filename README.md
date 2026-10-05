# Navigara CLI

Backfill local Claude Code and Codex sessions into Navigara. The CLI rebuilds the
OpenTelemetry the tools would have sent live, so a backfilled session looks the same in
Navigara as a live one.

## Set up this computer

```sh
navigara setup
```

On macOS and Linux, the wizard checks your saved login and opens the browser when
sign-in is needed. Choose your organization, then select Claude Code/Codex,
contributor email, and history window. Review the preview before applying changes.
Close running tool sessions before taking the preview.

The default host is `https://app.navigara.com`. For an on-prem instance:

```sh
navigara setup --host https://navigara.example.com
```

The selected host and organization are saved privately. To sign in or select a
different organization without running setup, use `navigara login [--host ORIGIN]`.
An organization administrator must authorize creation of an analytics ingestor key.

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

Resume verifies saved file hashes and requires the original host and organization.
Older manifests without an organization still ask for a token. Credentials are
stored privately in the account and tool settings, never in the upload manifest.

## Configure streaming only

```sh
navigara sessions config patch           # preview tool settings
navigara sessions config patch --apply   # apply for the saved host and organization
```

This command checks your login and organization without reading session history.
Preview creates no key and sends no telemetry. Apply verifies the receiver using
an empty request, then updates the selected tools (`--source all|claude|codex`).

## Backfill sessions

```sh
navigara sessions upload --last 30d                 # write OTLP request bodies, send nothing
navigara sessions upload --last 30d --send          # use saved login and organization
navigara sessions upload --last 30d --send \
  --url https://app.navigara.com/api --token "$NAVIGARA_TOKEN"
```

Explicit transport requires both an API base URL and an analytics ingestor key;
`NAVIGARA_TOKEN` supplies the token when set. Omit both to use the saved account.
Dry runs remain offline and use the local tool identity unless `--email` is supplied.

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
