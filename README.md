# Navigara CLI

Prepare local Claude Code and Codex session inventories for Navigara.

```sh
navigara --help
navigara --version
navigara sessions upload --dry-run --source all --format json --output ./navigara-sessions.json
```

`sessions upload` currently reports that extraction is unavailable, exits 1,
and creates no file. It performs no network requests.

Flags follow `upload` using standard Go flag syntax. `--dry-run` is required;
`--dry-run=false` is rejected. Source values are `all` (default), `claude`, and
`codex`; format values are `text` (default) and `json`. Output defaults to
`./navigara-sessions.json`.

`--claude-home DIR` and `--codex-home DIR` override tool homes containing
`projects/` and `sessions/`, respectively. Discovery will default to `~/.claude`
and `~/.codex`. Explicitly empty paths are invalid. Help and version use stdout;
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

Once extraction and writing are available, `text` stdout will show counts and
identities; `json` stdout will match the request envelope. The output file will
use exclusive creation with mode 0600 and refuse overwrite.
