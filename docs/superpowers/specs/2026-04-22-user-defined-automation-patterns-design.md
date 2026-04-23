# User-Defined Automation Patterns — Design

**Status:** Approved **Date:** 2026-04-22 **Tracking issue:** #370

## Goal

Let users append their own prefix patterns to the automated-session classifier
via `~/.agentsview/config.toml`, so personal tooling that issues recognizable
single-turn prompts is treated as automated without forking the binary.

## Background

The classifier in `internal/db/automated.go` currently ships with three
hardcoded slices: `automatedPrefixes`, `automatedSubstrings`, and
`automatedExactMatches`. `IsAutomatedSession(firstMessage)` returns true on any
prefix match, substring match, or exact match (after trim). The `is_automated`
flag is gated on `user_message_count <= 1` at write time and recomputed during a
one-shot backfill on `db.Open`, controlled by a manually-bumped marker
(currently `is_automated_backfill_v3`).

User patterns to support (motivating examples):

- `"You are analyzing an essay"`
- `"You are grading quotes"`
- `"You are analyzing a blog post"`
- `"Grade these Benn Stancil quotes"`

All four match as prefixes against the `first_message` column.

## Out of scope

- Substring and exact-match user patterns (YAGNI; revisit if a real use case
  appears).
- Per-project pattern overrides (samples are personal-tooling patterns that fire
  across repos).
- Hot-reload on config changes (restart is acceptable).
- Regex patterns (literal strings only).
- Removal or override of built-in patterns (additive only).

## Architecture

Five units, each with a clear single responsibility:

1. **Config schema** — TOML parsing, normalization, validation.
1. **Classifier singleton** — package-level state in `internal/db` that merges
   built-ins with the configured user prefixes.
1. **Classifier hash** — stable hash over (algorithm version + all built-in
   slices + user prefixes) used as the backfill trigger.
1. **Backfill driver (SQLite)** — replaces the version-keyed marker with a hash
   check against `stats`.
1. **Backfill driver (PostgreSQL)** — same hash mechanism against the PG
   `sync_metadata` table.

## Component details

### 1. Config schema

In `internal/config/config.go`:

```go
type AutomatedConfig struct {
    Prefixes []string `toml:"prefixes" json:"prefixes,omitempty"`
}

// On Config:
Automated AutomatedConfig `toml:"automated" json:"automated,omitempty"`
```

TOML usage:

```toml
[automated]
prefixes = [
  "You are analyzing an essay",
  "You are grading quotes",
]
```

Normalization at load (in `config.Load` after TOML unmarshalling):

- `strings.TrimSpace` each entry.
- Drop entries that become empty after trimming.
- Drop pattern entries longer than 1024 characters; log at warning level.
- Drop within-list duplicates (preserving first occurrence).
- Drop entries that exactly equal a built-in prefix; log at info level. This
  keeps the merged set tight and signals to the user that the pattern is already
  covered.

If `[automated]` is absent or has no entries, `Automated.Prefixes` is nil and
the classifier is unchanged from current behavior.

### 2. Classifier singleton

In `internal/db/automated.go`:

```go
var (
    userPrefixesMu sync.RWMutex
    userPrefixes   []string
)

// SetUserAutomationPrefixes replaces the user-pattern slice.
// The caller may pass nil to clear. Each entry is assumed to be
// pre-normalized by the caller (config layer enforces this).
func SetUserAutomationPrefixes(prefixes []string) {
    userPrefixesMu.Lock()
    defer userPrefixesMu.Unlock()
    userPrefixes = append([]string(nil), prefixes...)
}

// UserAutomationPrefixes returns a copy of the current slice.
// Used by ClassifierHash and tests.
func UserAutomationPrefixes() []string {
    userPrefixesMu.RLock()
    defer userPrefixesMu.RUnlock()
    return append([]string(nil), userPrefixes...)
}
```

`IsAutomatedSession` gains a third loop after the built-in prefix loop:

```go
func IsAutomatedSession(firstMessage string) bool {
    for _, prefix := range automatedPrefixes {
        if strings.HasPrefix(firstMessage, prefix) {
            return true
        }
    }
    userPrefixesMu.RLock()
    for _, prefix := range userPrefixes {
        if strings.HasPrefix(firstMessage, prefix) {
            userPrefixesMu.RUnlock()
            return true
        }
    }
    userPrefixesMu.RUnlock()
    // Existing substring + exact-match arms unchanged.
}
```

The `RWMutex` keeps the read path lock-free under contention; writes only happen
once at process start. Defensive copies on Set and Get prevent the caller from
mutating the singleton's backing array.

### 3. Classifier hash

New file `internal/db/classifier_hash.go`:

```go
const classifierAlgorithmVersion = 1

// ClassifierHash returns a stable hex-encoded SHA-256 over the
// algorithm version, all built-in pattern slices, and the
// currently configured user prefixes. Inputs are sorted before
// hashing so config order doesn't affect the result.
func ClassifierHash() string {
    h := sha256.New()
    fmt.Fprintf(h, "v%d\n", classifierAlgorithmVersion)
    writeSorted(h, "P", automatedPrefixes)
    writeSorted(h, "S", automatedSubstrings)
    writeSorted(h, "E", automatedExactMatches)
    writeSorted(h, "U", UserAutomationPrefixes())
    return hex.EncodeToString(h.Sum(nil))
}

func writeSorted(h hash.Hash, tag string, items []string) {
    sorted := append([]string(nil), items...)
    sort.Strings(sorted)
    for _, s := range sorted {
        fmt.Fprintf(h, "%s\t%d\t%s\n", tag, len(s), s)
    }
}
```

The tag prefix (`P`/`S`/`E`/`U`) and length-prefixed encoding prevent two
different inputs from producing the same hash by splicing across slice
boundaries.

`classifierAlgorithmVersion` is bumped manually when the matching *logic*
changes (e.g. a future case-insensitivity flag). It is the only remaining
manual-bump residue and lives next to the function that consumes it.

### 4. Backfill driver (SQLite)

`internal/db/db.go` changes:

- Remove the exported `IsAutomatedBackfillMarker` constant (was
  `"is_automated_backfill_v3"`). Internal-only; no external consumers.
- Replace `backfillIsAutomatedLocked` marker check with hash check:

```go
const classifierHashStatsKey = "is_automated_classifier_hash"

func (db *DB) backfillIsAutomatedLocked(w *sql.DB) error {
    current := ClassifierHash()
    var stored string
    if err := w.QueryRow(
        "SELECT value FROM stats WHERE key = ?",
        classifierHashStatsKey,
    ).Scan(&stored); err != nil && err != sql.ErrNoRows {
        return fmt.Errorf("probing classifier hash: %w", err)
    }
    if stored == current {
        return nil
    }
    // The existing set/clear loop in this function stays:
    // SELECT id, first_message, user_message_count, is_automated
    // → compute want = umc <= 1 && IsAutomatedSession(fm)
    // → batchUpdateAutomated for additions and clears
    // (already bumps local_modified_at for pg push pickup).
    // After that loop returns, write the hash:
    _, err := w.Exec(
        `INSERT INTO stats (key, value) VALUES (?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
        classifierHashStatsKey, current,
    )
    return err
}
```

The legacy `is_automated_backfill_v2` and `_v3` keys are left in place. Old code
reading them still sees `value=1` and skips its own backfill, providing the
downgrade safety described in the backward-compatibility analysis below. New
code never reads them.

Wiring: `cmd/agentsview/main.go` (and any other entry that loads config before
opening the DB) calls `db.SetUserAutomationPrefixes(cfg.Automated.Prefixes)`
immediately after `config.Load` and before `db.Open`. The order matters:
`db.Open` runs the backfill, which calls `ClassifierHash()`, which must see the
configured user prefixes.

### 5. Backfill driver (PostgreSQL)

`internal/postgres/schema.go` changes:

- Replace `isAutomatedBackfillMetadataKey = "is_automated_backfill_v3"` with
  `classifierHashMetadataKey = "is_automated_classifier_hash"`.
- `backfillIsAutomatedPG` follows the same hash-compare pattern, reading and
  writing against PG's `sync_metadata` table instead of SQLite's `stats` table.
- Hash input is the same `db.ClassifierHash()` — both stores see the same
  in-process classifier state because both run inside the same agentsview
  process.

PG-side flow when `pg push` propagates rows: SQLite-side `is_automated` values
are pushed directly. PG's own backfill only runs when the PG hash key differs
from the current process's hash, so it won't double-apply work the push already
did.

## Data flow

```
config.Load → AutomatedConfig.Prefixes (normalized slice)
            ↓
db.SetUserAutomationPrefixes(prefixes)   [process-start, once]
            ↓
db.Open
  └─ backfillIsAutomatedLocked
       ├─ ClassifierHash()  ← reads built-ins + user singleton
       ├─ compare to stats['is_automated_classifier_hash']
       └─ if differs: scan sessions, recompute is_automated, save hash

UpsertSession (per parsed session)
  └─ IsAutomatedSession(first_message) ← checks built-ins + user singleton

UpdateSessionIncremental (per file growth)
  └─ IsAutomatedSession(first_message) ← already added in PR #369
```

## Backward and forward compatibility

**Upgrade (existing DB → new code).** Stored: legacy `_v3` marker present, no
`is_automated_classifier_hash`. New code computes the current hash, sees no
stored value, runs backfill, stores hash. One extra backfill pass on first open
after upgrade — same cost as a manual `_vN` bump would have been, no user action
needed.

**Downgrade (new code → old code).** Old code only knows about `_v3` and finds
it set to `1`, so it skips its own backfill. The DB may carry classifications
that include user-pattern matches, but old code never *clears* `is_automated`,
only sets it. Worst case: more sessions look automated than old code would mark.
No corruption.

**Built-in pattern changes going forward.** No more manual marker bumps. Any
change to `automatedPrefixes`/`automatedSubstrings`/ `automatedExactMatches`
changes the hash, which triggers backfill on next open. Removes the "did I
remember to bump the marker?" footgun from PR #369.

**Logic changes going forward.** Any change to `IsAutomatedSession` matching
semantics requires bumping `classifierAlgorithmVersion` so the hash changes. The
constant lives in the same file as `ClassifierHash` so the bump is visible at
code-review time.

## Validation behavior

| Input                         | Behavior                                  |
| ----------------------------- | ----------------------------------------- |
| Missing `[automated]` section | Empty user prefix list (current behavior) |
| Empty `prefixes = []`         | Empty user prefix list                    |
| Whitespace-only entry         | Trimmed; if empty, dropped silently       |
| Duplicate within user list    | First occurrence kept, rest dropped       |
| Exact duplicate of built-in   | Dropped; logged at info level             |
| Pattern length > 1024 chars   | Dropped; logged at warning level          |
| Non-string entry (TOML error) | TOML decoder reports parse error          |

## Testing

| File                                               | Coverage                                                                           |
| -------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `internal/config/config_test.go`                   | TOML round-trip; normalization (trim/dedupe/empty/length-cap/built-in-overlap)     |
| `internal/db/automated_test.go`                    | New table-driven cases that set user prefixes, classify, then reset                |
| `internal/db/classifier_hash_test.go` (new)        | Hash stable across runs; differs when user list changes; differs across algo bumps |
| `internal/db/automated_backfill_test.go`           | Backfill no-ops when hash matches; runs and updates hash when it differs           |
| `internal/postgres/automated_pgtest_test.go` (new) | PG backfill parity under `pgtest` build tag                                        |
| `cmd/agentsview/main_test.go`                      | Integration: load config with user prefixes, verify singleton is wired before Open |

All new tests follow the existing table-driven Go convention in this repo.
SQLite tests use `testDB(t)` from `internal/db/db_test.go`.

## Files touched

| File                                               | Change type                               |
| -------------------------------------------------- | ----------------------------------------- |
| `internal/config/config.go`                        | Add struct, parsing, validation           |
| `internal/config/config_test.go`                   | Tests                                     |
| `internal/db/automated.go`                         | Singleton + IsAutomatedSession update     |
| `internal/db/automated_test.go`                    | Tests                                     |
| `internal/db/classifier_hash.go` (new)             | Hash function                             |
| `internal/db/classifier_hash_test.go` (new)        | Tests                                     |
| `internal/db/db.go`                                | Backfill marker → hash                    |
| `internal/db/automated_backfill_test.go`           | Tests                                     |
| `internal/postgres/schema.go`                      | PG marker → hash                          |
| `internal/postgres/automated_pgtest_test.go` (new) | Tests                                     |
| `cmd/agentsview/main.go`                           | Wire singleton from config before db.Open |
| `cmd/agentsview/main_test.go` (or new)             | Integration test                          |

## Risks and mitigations

| Risk                                                                                       | Mitigation                                                                                                                                                  |
| ------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Singleton state leaks across `go test` runs                                                | Reset helper in test; tests using user prefixes use it                                                                                                      |
| User accidentally adds a pattern that matches a common user prompt and clobbers their feed | Built-in patterns aren't override-able; user prefixes are additive only. Worst case: user-defined false positive, fixable by editing config and restarting. |
| Hash collision (two different pattern sets → same hash)                                    | SHA-256 + length-prefixed encoding makes this cryptographically negligible                                                                                  |
| Forgetting to bump `classifierAlgorithmVersion` on a logic change                          | Constant lives next to `ClassifierHash`; reviewers see both together                                                                                        |
