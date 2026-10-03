# Navigara CLI

Prepare local Claude Code and Codex session inventories for Navigara.

```sh
navigara --help
navigara --version
navigara sessions upload --dry-run --source all --format json --output ./navigara-sessions.json
```

`sessions upload` scans local histories and writes a request with exclusive
creation and mode 0600. Existing files and symlinks are refused; parent
directories must already exist. It performs no network requests.

Flags follow `upload` using standard Go flag syntax. `--dry-run` is required;
`--dry-run=false` is rejected. Source values are `all` (default), `claude`, and
`codex`; format values are `text` (default) and `json`. Output defaults to
`./navigara-sessions.json`.

`--claude-home DIR` and `--codex-home DIR` override tool homes. Claude histories
live under `projects/`; Codex histories live under `sessions/` and
`archived_sessions/`. Local discovery defaults to `~/.claude` and `~/.codex`.
An existing home without these history directories yields an empty inventory;
an explicitly supplied missing home yields a scan error. Explicitly empty paths
are invalid. Help and version use stdout;
diagnostics use stderr. Exit codes: 0 success, 1 extraction/output failure or
incomplete scan, 2 invalid invocation.

The request contract uses `schema_version: 1` and a `sessions` array, including
`[]` for an empty inventory. This illustrative envelope contains only session
identity and optional observation bounds/version:

```json
{
  "schema_version": 1,
  "sessions": [
    {
      "source": "claude_code",
      "session_id": "native-session-id",
      "first_observed_at": "2026-10-03T08:00:00Z",
      "last_observed_at": "2026-10-03T08:05:00Z",
      "source_version": "recorded-version"
    }
  ]
}
```

CLI `claude` maps to payload `claude_code`; `codex` maps to `codex`. IDs remain
native opaque values; local ID agreement does not establish OTel compatibility.
Only root sessions count. Optional times are RFC3339 UTC observation bounds,
not session start/end events. Missing fields are omitted, never inferred from
filesystem times or installed binaries. Payloads exclude paths and content.

`text` stdout shows discovered, emitted, duplicate, skipped, and failed counts,
completeness, and sorted source/native IDs. `json` stdout matches the file bytes;
its summary goes to stderr. Exact metadata copies of a source/ID emit once.
Conflicting copies exclude the entire group and count all copies as failed.

Malformed files and root/directory errors produce exit 1 with an incomplete
summary, while valid sessions still appear in the output. Root errors do not
increase failed file counts. Empty complete scans write `sessions: []` and exit 0.
Reader memory scales with the largest JSONL record. Each history is read only
up to its initial file size, excluding later appends. Output failures return 1;
a stdout failure preserves the completed request file.
