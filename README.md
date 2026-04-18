# auto-improve

Self-improving harness pipeline for AI coding agents.
Observes merged PRs, replays each under the current "best" rule set, scores the
output, proposes new rules, verifies them in a second pass, and promotes wins.

See design & implementation plan:

- `docs/migration-plan/memos/2026-04-17_自動改善パイプライン全体設計.md`
- `docs/migration-plan/memos/2026-04-17_自動改善パイプライン実装計画.md`

This package is **M1: the skeleton only** — config loader, state log, environment
preflight, and merged-PR detection. Step 10–70 arrive in M2+.

---

## Setup

1. **Install dependencies** (first time only):

   ```bash
   cd auto-improve
   pnpm install
   ```

2. **Copy the example config** and edit it for your environment:

   ```bash
   cp config.yaml.example config.yaml
   $EDITOR config.yaml
   ```

   At minimum, check:

   - `repo.github` — `owner/name`
   - `repo.best_branch` — the branch that holds your current best rules
   - `worktree.base` — a writable parent directory for per-PR worktrees
   - `paths.harness_files` — files/dirs copied into each worktree

3. **Run preflight** to verify the environment is ready:

   ```bash
   pnpm auto-improve preflight        # ASCII table on TTY
   pnpm auto-improve preflight --json # machine-readable (also default in non-TTY)
   ```

   This is a **hard gate**: non-zero exit if any required tool is missing, `gh`
   is not authenticated, or `config.yaml` fails schema validation. Warnings
   (e.g. harness files not yet present) do not block.

4. **Detect unprocessed merged PRs**:

   ```bash
   pnpm auto-improve detect-merged
   ```

   Prints JSON to stdout. Empty `prs: []` means there's nothing new to do.

   `detect-merged` runs its own critical-preflight check first and exits `2` if
   anything essential is missing (node/git/gh-auth/jq/yq/config.yaml/…).
   Pass `--skip-preflight` to bypass for local debugging.

   Pipe-friendly usage:

   ```bash
   pnpm auto-improve detect-merged | jq '.prs[].number'
   ```

---

## Commands

| Command                         | Purpose                                              | Exit codes               |
| ------------------------------- | ---------------------------------------------------- | ------------------------ |
| `pnpm auto-improve preflight`   | Check Node, pnpm, git, gh (+auth), jq, yq, claude,   | `0` OK, `1` NG, `2` arg  |
|                                 | codex (optional), config schema, writable dirs.      |                          |
| `pnpm auto-improve detect-merged` | List merged PRs not yet recorded terminal in state. | `0` OK, `1` runtime      |
| `pnpm auto-improve help`        | Show usage.                                          | `0`                      |
| `pnpm auto-improve version`     | Print version.                                       | `0`                      |

### Environment overrides

| Variable                   | Purpose                                                            |
| -------------------------- | ------------------------------------------------------------------ |
| `AUTO_IMPROVE_REPO_ROOT`   | Force repo root (skip `git rev-parse` autodetect). Used in tests.  |
| `AUTO_IMPROVE_CONFIG`      | Path to `config.yaml` (default: `<repo>/auto-improve/config.yaml`) |
| `LOG_LEVEL`                | `trace`/`debug`/`info`/`warn`/`error`/`fatal` (default `info`)     |

---

## State model — `processed.jsonl`

Append-only event log keyed by PR number. Each cycle step writes at least one
line. Idempotency is derived by replaying the log.

Event vocabulary (`lib/state.ts`):

```
started / step_done / promoting / promoted / rollback / failed / timeout / skipped / completed
```

Example:

```jsonl
{"pr":74,"event":"started","at":"2026-04-17T10:00:00.000Z","run_id":"pr74-20260417T100000Z"}
{"pr":74,"event":"step_done","at":"2026-04-17T10:03:12.000Z","run_id":"pr74-…","step":10}
{"pr":74,"event":"step_done","at":"…","run_id":"pr74-…","step":20,"manifest":"…/20-pass1/manifest.json"}
{"pr":74,"event":"promoted","at":"…","run_id":"pr74-…","adopted":true,"run_dir":"auto-improve/runs/2026-04-17-PR74"}
```

A PR is "done" (and will be skipped by `detect-merged`) once any terminal event
is recorded: `promoted | rollback | skipped | failed | completed | timeout`.

Crash recovery: a PR with only `started` on it will appear in the next
`detect-merged` output and can be retried. `timeout` is terminal on purpose —
a timed-out PR is **not** auto-retried; the operator must investigate and
manually re-queue (delete its terminal row, or re-run after the root cause is fixed).

### Detection window

`detect-merged` resolves the `gh pr list --search "merged:>=…"` floor in this order:

1. `detect.since` (absolute `YYYY-MM-DD` in config) — always wins if set
2. `max(N days ago, oldest-non-terminal-PR from state)` — widens automatically
   when the pipeline has been down longer than `lookback_days`
3. Clamped by `max_lookback_days` (default 180d) as a safety rail

The output JSON includes `since_source` so you can tell which rule fired.

---

## Directory layout

```
auto-improve/
├── README.md                     # this file
├── config.yaml.example           # committed; copy to config.yaml
├── config.yaml                   # gitignored, user-supplied
├── package.json
├── tsconfig.json
├── .gitignore
├── src/
│   ├── bin/auto-improve.ts       # CLI router
│   ├── preflight.ts              # env + config hard gate
│   ├── detect-merged.ts          # gh-based differential polling
│   └── lib/
│       ├── config.ts             # zod schema + loader
│       ├── logger.ts             # pino
│       ├── paths.ts              # repo-root / path resolution
│       └── state.ts              # processed.jsonl reader/appender
├── processed.jsonl               # (gitignored) append-only event log
└── runs/                         # (gitignored) per-PR run artifacts (M2+)
```

`runs/`, `processed.jsonl`, `rules-registry.jsonl`, and `config.yaml` are
intentionally gitignored. `pnpm-lock.yaml` and `config.yaml.example` are
committed.

---

## Development

```bash
pnpm typecheck     # tsc --noEmit
pnpm test          # vitest (no tests yet in M1)
```

### Code conventions

- ESM-only (`"type": "module"`).
- Run directly with `tsx` — no build step.
- All external I/O (config.yaml, processed.jsonl, gh JSON) passes through a
  zod schema. On validation failure, produce a readable multi-line error.
- `pino` for all logs; machine output (JSON results) goes to stdout, logs to
  stderr.

---

## What's in M1 (and what isn't)

Scoped in:

- `config.yaml.example` + zod schema + loader
- repo-root auto-detect + path resolution
- `processed.jsonl` schema + append/read/summarize (Codex M2 event set)
- preflight hard gate (node/pnpm/git/gh-auth/jq/yq/claude; codex optional)
- `detect-merged` via `gh pr list --search`

Deferred to M2+:

- `step 10` base restore + task-package.json
- `step 20/50` agent-parallel implement with manifest.json
- `step 30/60` scoring & pairwise (panel + codex arbiter rotation)
- `step 40` candidate extraction (strict JSON)
- `step 70` transactional promotion + rollback
- `rules-registry.jsonl`
- `run-cycle2.sh` / `run-cycle3.sh` orchestrators
- archives/removed, archives/rejected lifecycle
