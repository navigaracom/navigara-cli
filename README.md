# Navigara CLI

Export local Claude Code and Codex token usage as OTLP/HTTP JSON for Navigara.

```sh
navigara --help
navigara --version
navigara sessions upload --source all --output ./navigara-backfill
```

`sessions upload` creates a new directory with mode 0700, containing bounded
`metrics-000001.json`, `metrics-000002.json`, … request bodies with mode 0600.
Existing output paths and symlinks are refused; parent directories must exist.
Each body is at most 4 MiB and can be POSTed unchanged to the existing
`/otlp/v1/metrics` endpoint. The command performs no network requests or authentication. Token metrics are
supported; session events are not yet exported.

```sh
curl --fail -H "Authorization: Bearer $NAVIGARA_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @navigara-backfill/metrics-000001.json \
  "$NAVIGARA_URL/otlp/v1/metrics"
```

Send each generated body separately using credentials accepted by that endpoint.

Flags follow `upload` using standard Go flag syntax. `--dry-run` defaults to true;
`--dry-run=false` is unsupported. Source values are `all` (default), `claude`, and
`codex`. Output defaults to `./navigara-backfill`.

`--claude-home DIR` and `--codex-home DIR` override tool homes. Claude histories
live under `projects/`; Codex histories live under `sessions/` and
`archived_sessions/`. Local discovery defaults to `~/.claude` and `~/.codex`.
An existing home without these history directories yields an empty inventory;
an explicitly supplied missing home yields a scan error. Explicitly empty paths
are invalid. Help and version use stdout;
diagnostics use stderr. Exit codes: 0 success, 1 extraction/output failure or
incomplete scan, 2 invalid invocation.

Claude message counters become delta sums; identical repeated snapshots contribute
once, and conflicting snapshots remain ambiguous. Codex requires adjacent totals
whose difference matches the current last-usage snapshot, with unchanged model
and turn context. Its first snapshot is a baseline and emits no points. Missing
context, resets, unsafe values, and ambiguous usage are omitted and counted. Explicit zeros
are retained. No cumulative start time, identity, cost, or complete billing
coverage is inferred. OTLP bodies contain only mapped usage and recorded repository
URLs, excluding local paths and message content.
Overlap with previously ingested live telemetry is not yet reconciled.

Recorded repository URLs are attached to usage when available. Credentials,
queries, and local paths are stripped. No current git state is inferred.

Stdout shows discovered and selected session counts, duplicate/skipped/failed
counts, completeness, sorted source/native IDs, point and batch counts, and
mapped/repeated/ambiguous/unmapped observation counts. Native IDs are not invented.
Copies with identical complete recorded metadata for a source/ID count once;
conflicting copies exclude the entire group and count all copies as failed.

Malformed files and root/directory errors produce exit 1 with an incomplete
summary, while valid usage is still exported. Root errors do not increase failed
file counts. Empty complete scans create an empty output directory and exit 0.
Memory scales with retained observations and bounded encoding batches. Each
history is read only up to its initial size, excluding later appends. Output
failures return 1; completed batches remain if a later batch fails. A failed
individual file write removes that incomplete file. Stdout failure preserves
completed output.
