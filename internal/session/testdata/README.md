# Source-reader fixtures

These fixtures are allowlisted projections of local completed Claude Code
and Codex histories, captured read-only. No conversation, tool, or command
content is retained. Private provenance and source hashes remain outside git.

Claude root producer: 2.1.285; sidechain producer: 2.1.284.
Codex root producer: 0.139.0 (`exec` string source); child producer: 0.147.0.
Claude retains type, sidechain flag, sessionId, timestamp, version, and cwd,
including a genuine identity-free file-history-snapshot row.
Codex retains type, timestamps, metadata IDs/version/cwd/source, and child parent
markers. The child source retains subagent/thread_spawn/parent_thread_id/depth;
other nested fields are removed. IDs are consistently replaced, cwd becomes
/workspace/example, and timestamps become fixed chronological UTC values.
Tests explicitly mutate these projections for invalid input and boundary cases.

Codex usage producer: 0.156.1. `codex-usage.jsonl` retains four observed rows:
session metadata, turn context, and two token-count snapshots. IDs and timestamps
are consistently sanitized; native counter presence, zero values, and snapshot
relationships are preserved. The reader retains total and last snapshots in file
order without computing deltas or attributing cumulative totals to the context
model. Tests label unobserved repetitions, model changes, resets, and invalid
counters as synthetic. No new real history reads are needed to replay this fixture.
