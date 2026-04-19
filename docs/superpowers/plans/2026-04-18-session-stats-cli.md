# agentsview `session stats` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a window-scoped session analytics subcommand
`agentsview session stats` that emits a documented JSON schema suitable for
downstream consumers (tkmx-client, future dashboards).

**Architecture:** New Cobra subcommand under the existing `session` group
(`cmd/agentsview/session.go`). Flags mirror the HTTP query-param convention used
by `session list`. Command delegates to a new `SessionService.Stats()` method;
direct backend computes the result from a new `internal/db` package function.
Output is either human-readable or `--format json`, matching the parent group's
inherited flag.

**Tech Stack:** Go, Cobra, libsql/SQLite (existing), standard library `os/exec`
for git integration, `gh` CLI for PR queries.

**Spec reference:**
`~/code/tkmx-server/docs/superpowers/specs/2026-04-18-session-analytics-design.md`
— specifically the "JSON output schema (v1)", "Bucket boundaries", "Archetype
classification", and "Git integration" sections.

______________________________________________________________________

## File structure

| File                                             | Responsibility                                                                         |
| ------------------------------------------------ | -------------------------------------------------------------------------------------- |
| `cmd/agentsview/session_stats.go`                | New — Cobra command wiring + flag parsing + output formatting                          |
| `cmd/agentsview/session_stats_test.go`           | New — CLI integration tests (golden file for JSON output)                              |
| `internal/service/service.go`                    | Modify — add `Stats(ctx, StatsFilter)` to `SessionService` interface                   |
| `internal/service/stats_types.go`                | New — `StatsFilter` and response types that service.SessionStats wraps db.SessionStats |
| `internal/service/direct.go`                     | Modify — implement `Stats` on `directBackend`                                          |
| `internal/service/http.go`                       | Modify — implement `Stats` on `httpBackend` (stub OK for v1)                           |
| `internal/db/session_stats.go`                   | New — `GetSessionStats(ctx, f) (*SessionStats, error)` + all helpers                   |
| `internal/db/session_stats_test.go`              | New — table-driven tests for each schema section                                       |
| `internal/db/session_stats_buckets.go`           | New — bucket boundary definitions + bucket-assignment helpers                          |
| `internal/db/session_stats_types.go`             | New — all JSON-tagged output types                                                     |
| `internal/db/git/repos.go`                       | New — repo discovery from cwds                                                         |
| `internal/db/git/log.go`                         | New — `git log --numstat` parsing                                                      |
| `internal/db/git/pr.go`                          | New — `gh pr list` aggregation                                                         |
| `internal/db/git/cache.go`                       | New — SQLite-backed TTL cache for git results                                          |
| `internal/db/schema.sql`                         | Modify — add `git_cache` table for TTL cache                                           |
| `internal/db/testdata/session_stats_golden.json` | New — golden file for integration test                                                 |

Why split across multiple files: session_stats logic is substantial
(distributions, archetype, velocity, tool mix, temporal, git). Keeping each
concern in its own file keeps units small enough to reason about in context. Git
integration gets its own subpackage because it's external-process-dependent and
has its own tests.

______________________________________________________________________

## Task ordering rationale

Each task is self-contained and ends with a commit. Tasks within a phase can be
reordered; phases should proceed in order because later tasks depend on types
defined earlier.

- **Phase 1 (T1–T3):** Scaffolding — types, service plumbing, command wiring.
  Produces a working `session stats` command that returns an empty
  `SessionStats` object.
- **Phase 2 (T4–T13):** Per-section analytics. Each task implements one section
  of the JSON schema. Order within the phase follows the spec's schema structure
  top-to-bottom.
- **Phase 3 (T14–T17):** Git integration in its own sub-package.
- **Phase 4 (T18–T20):** CLI output formatting and integration tests.

______________________________________________________________________

## Phase 1 — Scaffolding

### Task 1: Declare output types in `internal/db/session_stats_types.go`

**Files:**

- Create: `internal/db/session_stats_types.go`

- [ ] **Step 1: Write the file with all schema types.**

Reference the spec's JSON schema (v1) in `session-analytics-design.md`. Each
JSON field gets a struct with matching json tags. Use pointer types for fields
that should be omitted when absent (e.g., `*Outcomes` for when Claude data is
missing).

```go
// internal/db/session_stats_types.go
package db

// SessionStats is the top-level v1 output of GetSessionStats.
// schema_version is locked at 1 for this CLI release; changes to
// bucket boundaries or field semantics require a version bump.
type SessionStats struct {
    SchemaVersion  int                       `json:"schema_version"`
    Window         StatsWindow               `json:"window"`
    Filters        StatsFilters              `json:"filters"`
    Totals         StatsTotals               `json:"totals"`
    Distributions  StatsDistributions        `json:"distributions"`
    Archetypes     StatsArchetypes           `json:"archetypes"`
    Velocity       StatsVelocity             `json:"velocity"`
    ToolMix        StatsToolMix              `json:"tool_mix"`
    ModelMix       StatsModelMix             `json:"model_mix"`
    Adoption       *StatsAdoption            `json:"adoption,omitempty"`
    AgentPortfolio StatsAgentPortfolio       `json:"agent_portfolio"`
    CacheEconomics *StatsCacheEconomics      `json:"cache_economics,omitempty"`
    Temporal       StatsTemporal             `json:"temporal"`
    OutcomeStats   *StatsOutcomeStats        `json:"outcome_stats,omitempty"`
    Outcomes       *StatsOutcomes            `json:"outcomes,omitempty"`
    GeneratedAt    string                    `json:"generated_at"`
}

type StatsWindow struct {
    Since string `json:"since"`
    Until string `json:"until"`
    Days  int    `json:"days"`
}

type StatsFilters struct {
    Agent             string   `json:"agent"`
    ProjectsIncluded  []string `json:"projects_included,omitempty"`
    ProjectsExcluded  []string `json:"projects_excluded"`
    Timezone          string   `json:"timezone"`
}

type StatsTotals struct {
    SessionsAll        int `json:"sessions_all"`
    SessionsHuman      int `json:"sessions_human"`
    SessionsAutomation int `json:"sessions_automation"`
    MessagesTotal      int `json:"messages_total"`
    UserMessagesTotal  int `json:"user_messages_total"`
}

type DistributionBucketV1 struct {
    // Edge is [lo, hi]; hi may be JSON null for the unbounded top bucket.
    Edge  [2]*float64 `json:"edge"`
    Count int         `json:"count"`
}

type ScopedDistribution struct {
    Buckets []DistributionBucketV1 `json:"buckets"`
    Mean    float64                `json:"mean"`
}

type StatsDistributions struct {
    DurationMinutes    ScopedDistributionPair       `json:"duration_minutes"`
    UserMessages       ScopedDistributionPair       `json:"user_messages"`
    PeakContextTokens  PeakContextDistribution      `json:"peak_context_tokens"`
    ToolsPerTurn       ScopedDistributionPair       `json:"tools_per_turn"`
}

type ScopedDistributionPair struct {
    ScopeAll   ScopedDistribution `json:"scope_all"`
    ScopeHuman ScopedDistribution `json:"scope_human"`
}

type PeakContextDistribution struct {
    ScopeAll   ScopedDistribution `json:"scope_all"`
    ScopeHuman ScopedDistribution `json:"scope_human"`
    NullCount  int                `json:"null_count"`
    ClaudeOnly bool               `json:"claude_only"`
}

type StatsArchetypes struct {
    Automation   int    `json:"automation"`
    Quick        int    `json:"quick"`
    Standard     int    `json:"standard"`
    Deep         int    `json:"deep"`
    Marathon     int    `json:"marathon"`
    Primary      string `json:"primary"`
    PrimaryHuman string `json:"primary_human"`
}

type StatsPercentiles struct {
    P50  float64 `json:"p50"`
    P90  float64 `json:"p90"`
    Mean float64 `json:"mean"`
}

type StatsVelocity struct {
    TurnCycleSeconds      StatsPercentiles `json:"turn_cycle_seconds"`
    FirstResponseSeconds  StatsPercentiles `json:"first_response_seconds"`
    MessagesPerActiveHour float64          `json:"messages_per_active_hour"`
}

type StatsToolMix struct {
    ByCategory map[string]int `json:"by_category"`
    TotalCalls int            `json:"total_calls"`
}

type StatsModelMix struct {
    ByTokens map[string]int64 `json:"by_tokens"`
}

type StatsAdoption struct {
    ClaudeOnly           bool    `json:"claude_only"`
    PlanModeRate         float64 `json:"plan_mode_rate"`
    SubagentsPerSession  float64 `json:"subagents_per_session"`
    DistinctSkills       int     `json:"distinct_skills"`
}

type StatsAgentPortfolio struct {
    BySessions map[string]int   `json:"by_sessions"`
    ByTokens   map[string]int64 `json:"by_tokens"`
    ByMessages map[string]int   `json:"by_messages"`
    Primary    string           `json:"primary"`
}

type StatsCacheEconomics struct {
    ClaudeOnly              bool            `json:"claude_only"`
    CacheHitRatio           CacheHitRatioDistribution `json:"cache_hit_ratio"`
    DollarsSavedVsUncached  float64         `json:"dollars_saved_vs_uncached"`
    DollarsSpent            float64         `json:"dollars_spent"`
}

type CacheHitRatioDistribution struct {
    Overall float64                `json:"overall"`
    Buckets []DistributionBucketV1 `json:"buckets"`
}

type TemporalHourlyUTCEntry struct {
    TS            string `json:"ts"`             // RFC3339 at UTC hour boundary
    Sessions      int    `json:"sessions"`
    UserMessages  int    `json:"user_messages"`
}

type StatsTemporal struct {
    HourlyUTC         []TemporalHourlyUTCEntry `json:"hourly_utc"`
    ReporterTimezone  string                   `json:"reporter_timezone"`
}

type StatsOutcomeStats struct {
    ReposActive   int `json:"repos_active"`
    Commits       int `json:"commits"`
    LOCAdded      int `json:"loc_added"`
    LOCRemoved    int `json:"loc_removed"`
    FilesChanged  int `json:"files_changed"`
    PRsOpened     *int `json:"prs_opened,omitempty"`    // nil when gh not configured
    PRsMerged     *int `json:"prs_merged,omitempty"`
}

type StatsOutcomes struct {
    ClaudeOnly             bool           `json:"claude_only"`
    Success                int            `json:"success"`
    Failure                int            `json:"failure"`
    Unknown                int            `json:"unknown"`
    GradeDistribution      map[string]int `json:"grade_distribution"`
    ToolRetryRate          float64        `json:"tool_retry_rate"`
    CompactionsPerSession  float64        `json:"compactions_per_session"`
    AvgEditChurn           float64        `json:"avg_edit_churn"`
}
```

- [ ] **Step 2: Verify compiles.**

Run: `cd ~/code/agentsview && go build ./...` Expected: exit code 0, no output.

- [ ] **Step 3: Commit.**

```bash
git add internal/db/session_stats_types.go
git commit -m "db: add SessionStats output types for session-stats CLI"
```

______________________________________________________________________

### Task 2: Declare bucket boundaries in `internal/db/session_stats_buckets.go`

**Files:**

- Create: `internal/db/session_stats_buckets.go`

- Create: `internal/db/session_stats_buckets_test.go`

- [ ] **Step 1: Write the boundaries and bucket-assignment helpers.**

```go
// internal/db/session_stats_buckets.go
package db

import "math"

// Schema v1 bucket boundaries. Changing any of these requires a
// schema_version bump (see session-analytics-design spec).

// Half-open intervals [lo, hi). The last bucket has hi = +Inf.
var durationMinutesEdges = []float64{0, 1, 5, 20, 60, 120, math.Inf(1)}

// user_messages scope_all: [0,1), [1,2), [2,5], [6,15], [16,30], [31,50], [51,inf)
// user_messages scope_human: [2,5], [6,15], [16,30], [31,50], [51,inf) -- no automation buckets
// Represented as two separate edge lists for clarity.
var userMessagesEdgesAll   = []float64{0, 2, 6, 16, 31, 51, math.Inf(1)}
var userMessagesEdgesHuman = []float64{2, 6, 16, 31, 51, math.Inf(1)}

var peakContextEdges = []float64{0, 10_000, 50_000, 100_000, 150_000, 200_000, math.Inf(1)}
var toolsPerTurnEdges = []float64{0, 1, 2, 4, 7, 11, math.Inf(1)}
var cacheHitRatioEdges = []float64{0, 0.25, 0.5, 0.75, 0.95, 1.000001} // inclusive of 1.0

// assignBucket returns the index i such that edges[i] <= v < edges[i+1],
// or -1 if v < edges[0] or v >= edges[len-1] (shouldn't happen given Inf upper).
func assignBucket(edges []float64, v float64) int {
    for i := 0; i < len(edges)-1; i++ {
        if v >= edges[i] && v < edges[i+1] {
            return i
        }
    }
    return -1
}

// buildEmptyBuckets returns a pre-sized bucket slice matching edges[i]..edges[i+1].
// Top bucket's hi is represented as JSON null by leaving Edge[1] as nil pointer.
func buildEmptyBuckets(edges []float64) []DistributionBucketV1 {
    out := make([]DistributionBucketV1, 0, len(edges)-1)
    for i := 0; i < len(edges)-1; i++ {
        lo := edges[i]
        var hiPtr *float64
        if !math.IsInf(edges[i+1], 1) {
            hi := edges[i+1]
            hiPtr = &hi
        }
        loPtr := lo
        out = append(out, DistributionBucketV1{
            Edge:  [2]*float64{&loPtr, hiPtr},
            Count: 0,
        })
    }
    return out
}
```

- [ ] **Step 2: Write table-driven tests.**

```go
// internal/db/session_stats_buckets_test.go
package db

import (
    "math"
    "testing"
)

func TestAssignBucketDurationEdges(t *testing.T) {
    cases := []struct {
        v    float64
        want int
    }{
        {0, 0}, {0.5, 0}, {1, 1}, {4.999, 1}, {5, 2},
        {19.999, 2}, {20, 3}, {59.999, 3}, {60, 4},
        {120, 5}, {120.1, 5}, {9999, 5},
    }
    for _, c := range cases {
        got := assignBucket(durationMinutesEdges, c.v)
        if got != c.want {
            t.Errorf("durationMinutes v=%v: got %d, want %d", c.v, got, c.want)
        }
    }
}

func TestAssignBucketUserMessagesAll(t *testing.T) {
    cases := []struct {
        v    float64
        want int // index into userMessagesEdgesAll (7 edges → 6 buckets)
    }{
        {0, 0}, {1, 0}, {1.9, 0}, // scope_all bucket [0,2)
        {2, 1}, {5, 1}, {5.9, 1},   // [2,6)
        {6, 2}, {15.9, 2},          // [6,16)
        {16, 3}, {30.9, 3},
        {31, 4}, {50.9, 4},
        {51, 5}, {10000, 5},
    }
    for _, c := range cases {
        got := assignBucket(userMessagesEdgesAll, c.v)
        if got != c.want {
            t.Errorf("user_messages scope_all v=%v: got %d, want %d", c.v, got, c.want)
        }
    }
}

func TestBuildEmptyBucketsTopIsUnbounded(t *testing.T) {
    b := buildEmptyBuckets(durationMinutesEdges)
    if len(b) != 6 {
        t.Fatalf("want 6 buckets, got %d", len(b))
    }
    top := b[len(b)-1]
    if top.Edge[1] != nil {
        t.Errorf("top bucket hi should be nil (JSON null), got %v", *top.Edge[1])
    }
    if math.IsInf(*top.Edge[0], 1) {
        t.Errorf("top bucket lo should be finite, got +Inf")
    }
}
```

- [ ] **Step 3: Run tests.**

Run: `go test ./internal/db/ -run TestAssignBucket -v` Expected: 3 PASS.

- [ ] **Step 4: Commit.**

```bash
git add internal/db/session_stats_buckets.go internal/db/session_stats_buckets_test.go
git commit -m "db: lock v1 bucket boundaries with assignment helpers"
```

______________________________________________________________________

### Task 3: Add `StatsFilter` and `Stats()` to `SessionService`

**Files:**

- Create: `internal/service/stats_types.go`

- Modify: `internal/service/service.go`

- Modify: `internal/service/direct.go`

- Modify: `internal/service/http.go`

- [ ] **Step 1: Create `stats_types.go`.**

```go
// internal/service/stats_types.go
package service

import "github.com/wesm/agentsview/internal/db"

// StatsFilter mirrors the session-stats CLI flag set.
type StatsFilter struct {
    Since            string   `json:"since,omitempty"`
    Until            string   `json:"until,omitempty"`
    Agent            string   `json:"agent,omitempty"`
    IncludeProjects  []string `json:"include_projects,omitempty"`
    ExcludeProjects  []string `json:"exclude_projects,omitempty"`
    Timezone         string   `json:"timezone,omitempty"`
    GHToken          string   `json:"-"`
}

// SessionStats is the transport-neutral response type; currently just
// an alias for db.SessionStats (the database package already carries
// the full schema with json tags).
type SessionStats = db.SessionStats
```

- [ ] **Step 2: Add `Stats` method to `SessionService` interface in
  `service.go`.**

Add inside the interface block (after `Watch`):

```go
Stats(ctx context.Context, f StatsFilter) (*SessionStats, error)
```

- [ ] **Step 3: Stub implementations on both backends.**

In `direct.go`, add a method that delegates to `db.GetSessionStats`:

```go
func (d *directBackend) Stats(ctx context.Context, f StatsFilter) (*SessionStats, error) {
    return d.db.GetSessionStats(ctx, db.StatsFilter{
        Since:           f.Since,
        Until:           f.Until,
        Agent:           f.Agent,
        IncludeProjects: f.IncludeProjects,
        ExcludeProjects: f.ExcludeProjects,
        Timezone:        f.Timezone,
        GHToken:         f.GHToken,
    })
}
```

In `http.go`, add a stub that returns an unimplemented error for now:

```go
func (h *httpBackend) Stats(ctx context.Context, f StatsFilter) (*SessionStats, error) {
    return nil, errors.New("session stats over HTTP backend: not yet implemented")
}
```

- [ ] **Step 4: Create matching `db.StatsFilter` stub and empty
  `GetSessionStats`.**

Create `internal/db/session_stats.go`:

```go
// internal/db/session_stats.go
package db

import (
    "context"
    "time"
)

// StatsFilter mirrors the service-layer StatsFilter but lives in db
// because db functions take typed filters without cross-package deps.
type StatsFilter struct {
    Since           string
    Until           string
    Agent           string
    IncludeProjects []string
    ExcludeProjects []string
    Timezone        string
    GHToken         string
}

// GetSessionStats computes the v1 session-stats JSON response.
// Stub returns a mostly-empty response so the command compiles and
// callers can incrementally fill sections in subsequent tasks.
func (db *DB) GetSessionStats(ctx context.Context, f StatsFilter) (*SessionStats, error) {
    return &SessionStats{
        SchemaVersion: 1,
        Window:        StatsWindow{},
        Filters: StatsFilters{
            Agent:            orDefault(f.Agent, "all"),
            ProjectsIncluded: f.IncludeProjects,
            ProjectsExcluded: nonNilSlice(f.ExcludeProjects),
            Timezone:         f.Timezone,
        },
        GeneratedAt: time.Now().UTC().Format(time.RFC3339),
    }, nil
}

func orDefault(v, d string) string {
    if v == "" {
        return d
    }
    return v
}

func nonNilSlice(s []string) []string {
    if s == nil {
        return []string{}
    }
    return s
}
```

- [ ] **Step 5: Build.**

Run: `go build ./...` Expected: exit 0.

- [ ] **Step 6: Commit.**

```bash
git add internal/service/stats_types.go internal/service/service.go internal/service/direct.go internal/service/http.go internal/db/session_stats.go
git commit -m "service: scaffold session Stats method; db: stub GetSessionStats"
```

______________________________________________________________________

### Task 4: Wire `session stats` Cobra command

**Files:**

- Create: `cmd/agentsview/session_stats.go`

- Modify: `cmd/agentsview/session.go` — add
  `cmd.AddCommand(newSessionStatsCommand())`

- [ ] **Step 1: Write the command file.**

Model after `session_list.go`. Flags: `--since`, `--until`, `--agent`,
`--include-project` (StringArray), `--exclude-project` (StringArray),
`--timezone`, `--gh-token`. `--format` is inherited from the parent `session`
group.

```go
// cmd/agentsview/session_stats.go
// ABOUTME: `session stats` subcommand — window-scoped analytics
// ABOUTME: emitting the v1 SessionStats JSON schema.
package main

import (
    "encoding/json"
    "fmt"
    "os"

    "github.com/spf13/cobra"
    "github.com/wesm/agentsview/internal/service"
)

func newSessionStatsCommand() *cobra.Command {
    var (
        since, until, agent, timezone, ghToken string
        includeProjects, excludeProjects       []string
    )
    cmd := &cobra.Command{
        Use:          "stats",
        Short:        "Window-scoped session analytics (v1 schema)",
        Args:         cobra.NoArgs,
        SilenceUsage: true,
        RunE: func(cmd *cobra.Command, args []string) error {
            svc, cleanup, err := resolveService(cmd)
            if err != nil {
                return err
            }
            defer cleanup()

            if ghToken == "" {
                // Fall back to env; spec: GH_TOKEN or GITHUB_TOKEN.
                ghToken = os.Getenv("GH_TOKEN")
                if ghToken == "" {
                    ghToken = os.Getenv("GITHUB_TOKEN")
                }
            }

            stats, err := svc.Stats(cmd.Context(), service.StatsFilter{
                Since:           since,
                Until:           until,
                Agent:           agent,
                IncludeProjects: includeProjects,
                ExcludeProjects: excludeProjects,
                Timezone:        timezone,
                GHToken:         ghToken,
            })
            if err != nil {
                return err
            }
            if outputFormat(cmd) == "json" {
                return json.NewEncoder(cmd.OutOrStdout()).Encode(stats)
            }
            return printSessionStatsHuman(cmd.OutOrStdout(), stats)
        },
    }

    f := cmd.Flags()
    f.StringVar(&since, "since", "28d",
        "Start of window (duration like 28d, or YYYY-MM-DD)")
    f.StringVar(&until, "until", "",
        "End of window (YYYY-MM-DD; default: now)")
    f.StringVar(&agent, "agent", "all",
        "Filter by agent (claude, codex, cursor, ... or 'all')")
    f.StringArrayVar(&includeProjects, "include-project", nil,
        "Restrict to these projects (repeatable)")
    f.StringArrayVar(&excludeProjects, "exclude-project", nil,
        "Exclude these projects (repeatable)")
    f.StringVar(&timezone, "timezone", "",
        "Timezone for temporal (default: local system timezone)")
    f.StringVar(&ghToken, "gh-token", "",
        "GitHub token for PR aggregation (falls back to GH_TOKEN/GITHUB_TOKEN env)")
    return cmd
}

// printSessionStatsHuman renders a human-readable summary. Stub for now;
// real formatting happens in Task 19.
func printSessionStatsHuman(w interface{ Write([]byte) (int, error) }, stats *service.SessionStats) error {
    _, err := fmt.Fprintf(w, "SessionStats (schema_version=%d, %d sessions total)\n",
        stats.SchemaVersion, stats.Totals.SessionsAll)
    return err
}
```

- [ ] **Step 2: Register in `session.go`.**

Edit `cmd/agentsview/session.go` — after the other `AddCommand` calls in
`newSessionCommand()`:

```go
cmd.AddCommand(newSessionStatsCommand())
```

- [ ] **Step 3: Smoke test.**

Run: `go run ./cmd/agentsview session stats --format json` Expected: JSON blob
with `schema_version: 1`, mostly empty fields, `generated_at` populated.

Run: `go run ./cmd/agentsview session stats` Expected: one-line human summary.

- [ ] **Step 4: Commit.**

```bash
git add cmd/agentsview/session_stats.go cmd/agentsview/session.go
git commit -m "cmd: add 'session stats' subcommand shell"
```

______________________________________________________________________

## Phase 2 — Per-section analytics

Each task below implements one section of the v1 schema. All live in
`internal/db/session_stats.go` (or helper files in the same package). Each task:
extend `GetSessionStats`, add table-driven test, commit.

Helper that tasks 5–13 share — add once, at the top of `session_stats.go`:

```go
// windowBounds resolves Since/Until into absolute RFC3339 bounds and
// the day count in between. Handles "28d" duration syntax and bare
// YYYY-MM-DD dates. Timezone param picks the local day boundary.
func windowBounds(f StatsFilter, now time.Time) (from, to time.Time, days int, err error) {
    // Implementation: parse Until (default: now), parse Since (default:
    // 28 days before Until). Support formats: "Nd" (days), "Nh" (hours),
    // or "YYYY-MM-DD". See internal/timeutil for existing helpers.
    ...
}
```

Use existing `internal/timeutil` helpers where possible; add a new helper in
that package if one doesn't already exist.

### Task 5: Implement `totals` and `archetypes`

**Files:**

- Modify: `internal/db/session_stats.go`

- Modify: `internal/db/session_stats_test.go`

- [ ] **Step 1: Write failing test with a fixture DB.**

```go
// session_stats_test.go
func TestGetSessionStats_TotalsAndArchetypes(t *testing.T) {
    db := openTestDB(t)
    // Insert 5 sessions: 2 automation (user_message_count = 0, 1),
    //                    2 human deep (user_message_count = 20, 40),
    //                    1 marathon (user_message_count = 100).
    insertSession(t, db, sessionFixture{id: "s1", agent: "claude", userMsgs: 0, startedAt: hoursAgo(5)})
    insertSession(t, db, sessionFixture{id: "s2", agent: "claude", userMsgs: 1, startedAt: hoursAgo(5)})
    insertSession(t, db, sessionFixture{id: "s3", agent: "claude", userMsgs: 20, startedAt: hoursAgo(5)})
    insertSession(t, db, sessionFixture{id: "s4", agent: "claude", userMsgs: 40, startedAt: hoursAgo(5)})
    insertSession(t, db, sessionFixture{id: "s5", agent: "claude", userMsgs: 100, startedAt: hoursAgo(5)})

    stats, err := db.GetSessionStats(t.Context(), StatsFilter{Since: "28d"})
    if err != nil {
        t.Fatalf("unexpected err: %v", err)
    }
    if stats.Totals.SessionsAll != 5 {
        t.Errorf("sessions_all: got %d want 5", stats.Totals.SessionsAll)
    }
    if stats.Totals.SessionsAutomation != 2 {
        t.Errorf("sessions_automation: got %d want 2", stats.Totals.SessionsAutomation)
    }
    if stats.Totals.SessionsHuman != 3 {
        t.Errorf("sessions_human: got %d want 3", stats.Totals.SessionsHuman)
    }
    if stats.Archetypes.Automation != 2 { t.Errorf("auto: %d", stats.Archetypes.Automation) }
    if stats.Archetypes.Deep != 2 { t.Errorf("deep: %d", stats.Archetypes.Deep) }
    if stats.Archetypes.Marathon != 1 { t.Errorf("marathon: %d", stats.Archetypes.Marathon) }
    if stats.Archetypes.Primary != "automation" && stats.Archetypes.Primary != "deep" {
        // 2 automation, 2 deep, 1 marathon — tie broken by spec (document choice)
    }
    if stats.Archetypes.PrimaryHuman != "deep" {
        t.Errorf("primary_human: got %q want deep", stats.Archetypes.PrimaryHuman)
    }
}
```

(`openTestDB`, `insertSession`, `sessionFixture` should reuse existing helpers
in `session_stats_test.go` or neighboring test files — look in
`internal/db/analytics_test.go` for the established pattern.)

- [ ] **Step 2: Run test → FAIL.** Expected: `sessions_all: got 0 want 5` (the
  stub doesn't query anything).

- [ ] **Step 3: Implement.**

Add to `session_stats.go`:

```go
// archetypeLabel classifies a session by user_message_count.
func archetypeLabel(userMsgs int) string {
    switch {
    case userMsgs <= 1:
        return "automation"
    case userMsgs <= 5:
        return "quick"
    case userMsgs <= 15:
        return "standard"
    case userMsgs <= 50:
        return "deep"
    default:
        return "marathon"
    }
}

// loadSessionsInWindow selects the rows needed for stats computation.
// Returns a compact row struct; callers iterate to compute sections.
type sessionStatsRow struct {
    id                string
    agent             string
    project           string
    startedAt         time.Time
    endedAt           sql.NullTime
    messageCount      int
    userMessageCount  int
    totalOutputTokens int64
    peakContextTokens sql.NullInt64
    hasPeakContext    bool
    planModeEntered   int    // from tool_calls, see later task
    // ... add fields as later tasks need them
}

func (db *DB) loadSessionsInWindow(ctx context.Context, f StatsFilter) ([]sessionStatsRow, error) {
    // SQL: SELECT ... FROM sessions WHERE started_at >= ? AND started_at < ? [AND agent = ?] [AND project filters]
    ...
}
```

Replace `GetSessionStats` body:

```go
func (db *DB) GetSessionStats(ctx context.Context, f StatsFilter) (*SessionStats, error) {
    tz, err := resolveTimezone(f.Timezone)
    if err != nil {
        return nil, fmt.Errorf("resolving timezone: %w", err)
    }
    from, to, days, err := windowBounds(f, time.Now())
    if err != nil {
        return nil, fmt.Errorf("resolving window: %w", err)
    }

    rows, err := db.loadSessionsInWindow(ctx, f)
    if err != nil {
        return nil, err
    }

    stats := &SessionStats{
        SchemaVersion: 1,
        Window: StatsWindow{
            Since: from.UTC().Format(time.RFC3339),
            Until: to.UTC().Format(time.RFC3339),
            Days:  days,
        },
        Filters: StatsFilters{
            Agent:            orDefault(f.Agent, "all"),
            ProjectsIncluded: f.IncludeProjects,
            ProjectsExcluded: nonNilSlice(f.ExcludeProjects),
            Timezone:         tz.String(),
        },
        GeneratedAt: time.Now().UTC().Format(time.RFC3339),
    }

    computeTotalsAndArchetypes(stats, rows)
    // Subsequent tasks add: computeDistributions, computeVelocity, etc.

    return stats, nil
}

func computeTotalsAndArchetypes(s *SessionStats, rows []sessionStatsRow) {
    archMax := map[string]int{}
    humanMax := map[string]int{}
    for _, r := range rows {
        s.Totals.SessionsAll++
        s.Totals.MessagesTotal += r.messageCount
        s.Totals.UserMessagesTotal += r.userMessageCount

        label := archetypeLabel(r.userMessageCount)
        switch label {
        case "automation":
            s.Archetypes.Automation++
            s.Totals.SessionsAutomation++
        case "quick":
            s.Archetypes.Quick++
            s.Totals.SessionsHuman++
            humanMax[label]++
        case "standard":
            s.Archetypes.Standard++
            s.Totals.SessionsHuman++
            humanMax[label]++
        case "deep":
            s.Archetypes.Deep++
            s.Totals.SessionsHuman++
            humanMax[label]++
        case "marathon":
            s.Archetypes.Marathon++
            s.Totals.SessionsHuman++
            humanMax[label]++
        }
        archMax[label]++
    }
    s.Archetypes.Primary = pickMaxLabel(archMax, []string{"automation", "marathon", "deep", "standard", "quick"})
    s.Archetypes.PrimaryHuman = pickMaxLabel(humanMax, []string{"marathon", "deep", "standard", "quick"})
}

// pickMaxLabel returns the key with the highest count. Ties broken
// by the priority order passed in (earlier entries win).
func pickMaxLabel(counts map[string]int, priority []string) string {
    best := ""
    bestN := -1
    for _, k := range priority {
        if counts[k] > bestN {
            best = k
            bestN = counts[k]
        }
    }
    return best
}
```

- [ ] **Step 4: Run test → PASS.**

Run: `go test ./internal/db/ -run TestGetSessionStats_TotalsAndArchetypes -v`

- [ ] **Step 5: Commit.**

```bash
git add internal/db/session_stats.go internal/db/session_stats_test.go
git commit -m "db(stats): implement totals and archetype classification"
```

______________________________________________________________________

### Task 6: Implement `distributions` (duration, user_messages, tools_per_turn, peak_context)

**Files:**

- Modify: `internal/db/session_stats.go`

- Modify: `internal/db/session_stats_test.go`

- [ ] **Step 1: Write failing test.**

Test should seed sessions with known durations/message counts/peak contexts,
then assert bucket counts and means match expectations for both `scope_all` and
`scope_human`.

```go
func TestGetSessionStats_Distributions(t *testing.T) {
    db := openTestDB(t)
    // Helper: fixture(userMsgs, durationMin, peakCtx, toolsPerTurn)
    for _, f := range []struct {
        id string; userMsgs, peakCtx int; durMin float64; toolCalls int
    }{
        {"a", 0,  2_000,   0.5,  0},   // automation
        {"b", 1,  8_000,   0.9,  1},   // automation
        {"c", 3,  25_000,  10.0, 6},   // quick/human
        {"d", 10, 60_000,  25.0, 15},  // standard/human
        {"e", 30, 150_000, 120.0,30},  // deep/human
    } {
        insertSession(t, db, sessionFixture{
            id: f.id, agent: "claude", userMsgs: f.userMsgs,
            peakContext: f.peakCtx, durationMin: f.durMin,
            totalToolCalls: f.toolCalls, assistantTurns: f.userMsgs, // for tools/turn denominator
            startedAt: hoursAgo(10),
        })
    }

    stats, err := db.GetSessionStats(t.Context(), StatsFilter{Since: "28d"})
    if err != nil { t.Fatal(err) }

    // scope_all duration: 0.5→bucket0 [0,1), 0.9→bucket0, 10→bucket2 [5,20), 25→bucket3, 120→bucket5 (Inf)
    gotAll := stats.Distributions.DurationMinutes.ScopeAll.Buckets
    wantCountsAll := []int{2, 0, 1, 1, 0, 1}
    for i, w := range wantCountsAll {
        if gotAll[i].Count != w {
            t.Errorf("duration scope_all bucket %d: got %d want %d", i, gotAll[i].Count, w)
        }
    }
    // scope_human (excludes a, b): only c, d, e → [5,20):1, [20,60):1, [120,Inf):1
    gotHuman := stats.Distributions.DurationMinutes.ScopeHuman.Buckets
    wantCountsHuman := []int{0, 0, 1, 1, 0, 1}
    for i, w := range wantCountsHuman {
        if gotHuman[i].Count != w {
            t.Errorf("duration scope_human bucket %d: got %d want %d", i, gotHuman[i].Count, w)
        }
    }
    // Means:
    wantAllMean := (0.5 + 0.9 + 10 + 25 + 120) / 5
    if !floatsClose(stats.Distributions.DurationMinutes.ScopeAll.Mean, wantAllMean, 0.01) {
        t.Errorf("duration scope_all mean: got %v want %v", stats.Distributions.DurationMinutes.ScopeAll.Mean, wantAllMean)
    }

    // user_messages scope_all bucket 0 [0,2): a,b; bucket 1 [2,6): c; bucket 2 [6,16): d; bucket 4 [16,31): e
    // Wait — the scope_all edges are [0,2,6,16,31,51,Inf). e has userMsgs=30 → bucket 3 [16,31)
    // Confirm bucket assignments match userMessagesEdgesAll.

    // peak_context scope_human: c(25k), d(60k), e(150k) → buckets 1,2,3 of peakContextEdges
    gotPC := stats.Distributions.PeakContextTokens.ScopeHuman.Buckets
    if gotPC[1].Count != 1 || gotPC[2].Count != 1 || gotPC[3].Count != 1 {
        t.Errorf("peak_context scope_human: %+v", gotPC)
    }
}
```

- [ ] **Step 2: Run → FAIL** (Distributions fields empty).

- [ ] **Step 3: Implement
  `computeDistributions(s *SessionStats, rows []sessionStatsRow)`.**

```go
func computeDistributions(s *SessionStats, rows []sessionStatsRow) {
    // Initialize all bucket slices up-front to guarantee stable JSON output.
    s.Distributions.DurationMinutes = ScopedDistributionPair{
        ScopeAll:   ScopedDistribution{Buckets: buildEmptyBuckets(durationMinutesEdges)},
        ScopeHuman: ScopedDistribution{Buckets: buildEmptyBuckets(durationMinutesEdges)},
    }
    s.Distributions.UserMessages = ScopedDistributionPair{
        ScopeAll:   ScopedDistribution{Buckets: buildEmptyBuckets(userMessagesEdgesAll)},
        ScopeHuman: ScopedDistribution{Buckets: buildEmptyBuckets(userMessagesEdgesHuman)},
    }
    s.Distributions.PeakContextTokens = PeakContextDistribution{
        ScopeAll:   ScopedDistribution{Buckets: buildEmptyBuckets(peakContextEdges)},
        ScopeHuman: ScopedDistribution{Buckets: buildEmptyBuckets(peakContextEdges)},
        ClaudeOnly: true,
    }
    s.Distributions.ToolsPerTurn = ScopedDistributionPair{
        ScopeAll:   ScopedDistribution{Buckets: buildEmptyBuckets(toolsPerTurnEdges)},
        ScopeHuman: ScopedDistribution{Buckets: buildEmptyBuckets(toolsPerTurnEdges)},
    }

    var (
        sumDurAll, sumDurHuman       float64
        sumUmAll, sumUmHuman         float64
        sumPCAll, sumPCHuman         float64
        sumTPTAll, sumTPTHuman       float64
        nDurAll, nDurHuman           int
        nUmAll, nUmHuman             int
        nPCAll, nPCHuman             int
        nTPTAll, nTPTHuman           int
    )

    for _, r := range rows {
        isHuman := r.userMessageCount >= 2

        // duration
        if r.endedAt.Valid {
            mins := r.endedAt.Time.Sub(r.startedAt).Minutes()
            addBucket(s.Distributions.DurationMinutes.ScopeAll.Buckets, durationMinutesEdges, mins)
            sumDurAll += mins; nDurAll++
            if isHuman {
                addBucket(s.Distributions.DurationMinutes.ScopeHuman.Buckets, durationMinutesEdges, mins)
                sumDurHuman += mins; nDurHuman++
            }
        }

        // user_messages
        umF := float64(r.userMessageCount)
        addBucket(s.Distributions.UserMessages.ScopeAll.Buckets, userMessagesEdgesAll, umF)
        sumUmAll += umF; nUmAll++
        if isHuman {
            addBucket(s.Distributions.UserMessages.ScopeHuman.Buckets, userMessagesEdgesHuman, umF)
            sumUmHuman += umF; nUmHuman++
        }

        // peak_context (Claude-only + null_count)
        if r.agent == "claude" {
            if !r.hasPeakContext || !r.peakContextTokens.Valid {
                s.Distributions.PeakContextTokens.NullCount++
            } else {
                pc := float64(r.peakContextTokens.Int64)
                addBucket(s.Distributions.PeakContextTokens.ScopeAll.Buckets, peakContextEdges, pc)
                sumPCAll += pc; nPCAll++
                if isHuman {
                    addBucket(s.Distributions.PeakContextTokens.ScopeHuman.Buckets, peakContextEdges, pc)
                    sumPCHuman += pc; nPCHuman++
                }
            }
        }

        // tools_per_turn: totalToolCalls / max(1, assistantTurns). Need to add tool_call_count
        // and assistant_turn_count to sessionStatsRow; extend loadSessionsInWindow accordingly.
        tpt := 0.0
        if r.assistantTurns > 0 {
            tpt = float64(r.totalToolCalls) / float64(r.assistantTurns)
        }
        addBucket(s.Distributions.ToolsPerTurn.ScopeAll.Buckets, toolsPerTurnEdges, tpt)
        sumTPTAll += tpt; nTPTAll++
        if isHuman {
            addBucket(s.Distributions.ToolsPerTurn.ScopeHuman.Buckets, toolsPerTurnEdges, tpt)
            sumTPTHuman += tpt; nTPTHuman++
        }
    }

    s.Distributions.DurationMinutes.ScopeAll.Mean = safeMean(sumDurAll, nDurAll)
    s.Distributions.DurationMinutes.ScopeHuman.Mean = safeMean(sumDurHuman, nDurHuman)
    s.Distributions.UserMessages.ScopeAll.Mean = safeMean(sumUmAll, nUmAll)
    s.Distributions.UserMessages.ScopeHuman.Mean = safeMean(sumUmHuman, nUmHuman)
    s.Distributions.PeakContextTokens.ScopeAll.Mean = safeMean(sumPCAll, nPCAll)
    s.Distributions.PeakContextTokens.ScopeHuman.Mean = safeMean(sumPCHuman, nPCHuman)
    s.Distributions.ToolsPerTurn.ScopeAll.Mean = safeMean(sumTPTAll, nTPTAll)
    s.Distributions.ToolsPerTurn.ScopeHuman.Mean = safeMean(sumTPTHuman, nTPTHuman)
}

func addBucket(buckets []DistributionBucketV1, edges []float64, v float64) {
    i := assignBucket(edges, v)
    if i >= 0 && i < len(buckets) {
        buckets[i].Count++
    }
}

func safeMean(sum float64, n int) float64 {
    if n == 0 {
        return 0
    }
    return sum / float64(n)
}
```

- [ ] **Step 4: Call `computeDistributions(stats, rows)` in `GetSessionStats`
  after `computeTotalsAndArchetypes`.**

- [ ] **Step 5: Extend `sessionStatsRow` / `loadSessionsInWindow` to return
  `assistantTurns` and `totalToolCalls`.** These likely need a JOIN or
  correlated subquery against `messages`/`tool_calls`. Look at
  `GetAnalyticsTools` for the query shape.

- [ ] **Step 6: Run test → PASS.**

Run: `go test ./internal/db/ -run TestGetSessionStats_Distributions -v`

- [ ] **Step 7: Commit.**

```bash
git add internal/db/session_stats.go internal/db/session_stats_test.go
git commit -m "db(stats): implement scope-aware distribution histograms"
```

______________________________________________________________________

### Task 7: Implement `velocity`

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [ ] **Step 1: Write failing test** (seed two sessions with known message
  timestamps, assert p50/p90/mean turn cycle + first-response).

Use the same test-fixture pattern as Task 6. Insert messages rows with known
timestamps so `computeVelocity` can derive turn intervals.

- [ ] **Step 2: Reuse velocityAccumulator from `analytics.go`** (line 1747). It
  already computes TurnCycleSec/FirstResponseSec/MsgsPerActiveMin from
  per-message timestamps. Wrap it with a thin computeVelocity function that
  converts units (ActiveMin → ActiveHour) and includes `mean` alongside p50/p90.

```go
func computeVelocity(s *SessionStats, accum *velocityAccumulator) {
    ov := accum.computeOverview() // existing method
    s.Velocity.TurnCycleSeconds = StatsPercentiles{
        P50:  ov.TurnCycleSec.P50,
        P90:  ov.TurnCycleSec.P90,
        Mean: accum.turnCycleMean(),
    }
    s.Velocity.FirstResponseSeconds = StatsPercentiles{
        P50:  ov.FirstResponseSec.P50,
        P90:  ov.FirstResponseSec.P90,
        Mean: accum.firstResponseMean(),
    }
    if accum.activeMinutes > 0 {
        s.Velocity.MessagesPerActiveHour = float64(accum.totalMsgs) / (accum.activeMinutes / 60.0)
    }
}
```

Add `turnCycleMean()` and `firstResponseMean()` methods to `velocityAccumulator`
in `analytics.go`:

```go
func (a *velocityAccumulator) turnCycleMean() float64 {
    if len(a.turnCycles) == 0 { return 0 }
    sum := 0.0
    for _, v := range a.turnCycles { sum += v }
    return sum / float64(len(a.turnCycles))
}
// (same shape for firstResponseMean)
```

- [ ] **Step 3: Populate the accumulator from loadSessionsInWindow.** The
  existing `GetAnalyticsVelocity` does this work in `analytics.go:~1780`.
  Extract the accumulator-population loop into a helper
  `populateVelocityAccumulator(ctx, db, f) (*velocityAccumulator, error)` that
  both `GetAnalyticsVelocity` and the new `GetSessionStats` can call. Refactor
  with care — run `go test ./internal/db/` after the refactor to verify existing
  velocity tests still pass.

- [ ] **Step 4: Run the new test → PASS. Run existing tests → PASS.**

```bash
go test ./internal/db/ -run TestGetSessionStats_Velocity -v
go test ./internal/db/ -run TestGetAnalyticsVelocity -v
```

- [ ] **Step 5: Commit.**

```bash
git add internal/db/session_stats.go internal/db/session_stats_test.go internal/db/analytics.go
git commit -m "db(stats): implement velocity with mean + messages-per-active-hour"
```

______________________________________________________________________

### Task 8: Implement `tool_mix` and `model_mix`

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [ ] **Step 1: Write failing test** (seed tool_calls with known category
  distribution and messages with known model usage; assert both maps).

- [ ] **Step 2: Implement.**

```go
func (db *DB) computeToolAndModelMix(ctx context.Context, s *SessionStats, f StatsFilter) error {
    // tool_mix: reuse GetAnalyticsTools logic. It already groups by
    // category and totals calls; just reshape its output.
    toolResp, err := db.GetAnalyticsTools(ctx, AnalyticsFilter{
        From: f.Since, To: f.Until, Agent: f.Agent,
    })
    if err != nil { return err }
    s.ToolMix.ByCategory = map[string]int{}
    for _, cat := range toolResp.ByCategory {
        s.ToolMix.ByCategory[cat.Category] = cat.Count
        s.ToolMix.TotalCalls += cat.Count
    }

    // model_mix: sum total_output_tokens per model across messages in window.
    rows, err := db.getReader().QueryContext(ctx, `
        SELECT m.model, SUM(m.total_tokens)
        FROM messages m
        JOIN sessions s ON s.id = m.session_id
        WHERE s.started_at >= ? AND s.started_at < ? AND m.model != ''
        GROUP BY m.model
    `, f.Since, f.Until)
    // ... populate s.ModelMix.ByTokens
    return nil
}
```

Details depend on existing analytics.go shape — inspect `GetAnalyticsTools`
response struct for exact fields.

- [ ] **Step 3: Run → PASS.**

- [ ] **Step 4: Commit.**

```bash
git commit -am "db(stats): implement tool_mix and model_mix"
```

______________________________________________________________________

### Task 9: Implement `agent_portfolio`

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [ ] **Step 1: Failing test** with sessions across three agents (claude, codex,
  cursor); assert `by_sessions`, `by_messages`, and `primary`.

- [ ] **Step 2: Implement.**

```go
func computeAgentPortfolio(s *SessionStats, rows []sessionStatsRow) {
    bySessions := map[string]int{}
    byMessages := map[string]int{}
    byTokens   := map[string]int64{}
    for _, r := range rows {
        bySessions[r.agent]++
        byMessages[r.agent] += r.messageCount
        byTokens[r.agent]   += r.totalOutputTokens
    }
    s.AgentPortfolio.BySessions = bySessions
    s.AgentPortfolio.ByMessages = byMessages
    s.AgentPortfolio.ByTokens   = byTokens
    best := ""
    bestN := -1
    for k, v := range bySessions {
        if v > bestN { best = k; bestN = v }
    }
    s.AgentPortfolio.Primary = best
}
```

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db(stats): implement agent_portfolio"
```

______________________________________________________________________

### Task 10: Implement `cache_economics`

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [ ] **Step 1: Failing test.** Seed sessions with claude agent, known
  input/output/cache_read/cache_creation token values; assert cache_hit_ratio
  histogram + dollars_saved.

- [ ] **Step 2: Implement.** Per-session ratio =
  `cache_read / (input + cache_read + cache_creation)`. Use `GetDailyUsage`
  pricing helpers for dollar computation (analytics.go / usage.go). Dollars
  saved = cost_without_cache − actual_cost, where cost_without_cache reprices
  cache_read tokens as input tokens. Skip sessions for non-claude agents (set
  `cache_economics = nil` in output when no claude sessions).

```go
func (db *DB) computeCacheEconomics(ctx context.Context, s *SessionStats, rows []sessionStatsRow) error {
    claudeRows := filterByAgent(rows, "claude")
    if len(claudeRows) == 0 {
        return nil // leave stats.CacheEconomics nil
    }
    s.CacheEconomics = &StatsCacheEconomics{
        ClaudeOnly: true,
        CacheHitRatio: CacheHitRatioDistribution{
            Buckets: buildEmptyBuckets(cacheHitRatioEdges),
        },
    }
    // For each session, fetch its per-message token breakdown from messages
    // table. Sum the four token types, compute ratio, bucket it, and
    // accumulate dollar values via pricing.go helpers.
    ...
}
```

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db(stats): implement cache_economics with per-session hit-ratio histogram"
```

______________________________________________________________________

### Task 11: Implement `temporal` (hourly_utc + reporter_timezone)

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [ ] **Step 1: Failing test.** Seed messages with known UTC timestamps spanning
  a few hours; assert `hourly_utc` entries group correctly by UTC calendar hour.

- [ ] **Step 2: Implement.**

```go
func (db *DB) computeTemporal(ctx context.Context, s *SessionStats, f StatsFilter, rows []sessionStatsRow) error {
    // Query messages joined to sessions, aggregating message count and
    // session-start count per UTC hour.
    query := `
        SELECT
            strftime('%Y-%m-%dT%H:00:00Z', m.timestamp) AS utc_hour,
            COUNT(*) AS user_messages,
            COUNT(DISTINCT CASE WHEN m.role='user' THEN s.id ELSE NULL END) AS sessions
        FROM messages m
        JOIN sessions s ON s.id = m.session_id
        WHERE s.started_at >= ? AND s.started_at < ?
          AND m.role = 'user'
        GROUP BY utc_hour
        ORDER BY utc_hour
    `
    // ... populate s.Temporal.HourlyUTC
    s.Temporal.ReporterTimezone = localZoneName()
    return nil
}

func localZoneName() string {
    name, _ := time.Now().Zone()
    // For IANA, use time.Local.String() which returns "Local" — instead
    // prefer the env var or tzdata lookup. Simple approach for v1:
    if tz := os.Getenv("TZ"); tz != "" {
        return tz
    }
    return time.Local.String()
}
```

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db(stats): implement temporal.hourly_utc + reporter_timezone"
```

______________________________________________________________________

### Task 12: Implement `outcomes` (from existing signals)

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [ ] **Step 1: Failing test.** Seed claude sessions with varied `health_grade`,
  `outcome`, `tool_retry_count`, `compaction_count`, `edit_churn_count`; assert
  the aggregated `outcomes` fields.

- [ ] **Step 2: Implement.**

```go
func computeOutcomes(s *SessionStats, rows []sessionStatsRow) {
    claudeRows := filterByAgent(rows, "claude")
    if len(claudeRows) == 0 {
        return
    }
    out := &StatsOutcomes{
        ClaudeOnly:        true,
        GradeDistribution: map[string]int{},
    }
    totalTools := 0
    totalRetries := 0
    totalCompactions := 0
    totalChurn := 0
    for _, r := range claudeRows {
        switch r.outcome {
        case "success": out.Success++
        case "failure": out.Failure++
        default:        out.Unknown++
        }
        if r.healthGrade != "" {
            out.GradeDistribution[r.healthGrade]++
        }
        totalTools      += r.toolCallCount      // add to sessionStatsRow
        totalRetries    += r.toolRetryCount
        totalCompactions+= r.compactionCount
        totalChurn      += r.editChurnCount
    }
    if totalTools > 0 {
        out.ToolRetryRate = float64(totalRetries) / float64(totalTools)
    }
    if len(claudeRows) > 0 {
        out.CompactionsPerSession = float64(totalCompactions) / float64(len(claudeRows))
        out.AvgEditChurn = float64(totalChurn) / float64(len(claudeRows))
    }
    s.Outcomes = out
}
```

Extend `sessionStatsRow` and `loadSessionsInWindow` SQL to select `outcome`,
`health_grade`, `tool_retry_count`, `tool_failure_signal_count`,
`compaction_count`, `edit_churn_count`.

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db(stats): implement outcomes section (stored but not yet rendered by tkmx-server)"
```

______________________________________________________________________

### Task 13: Implement `adoption` (plan_mode_rate, subagents, distinct_skills)

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [ ] **Step 1: Failing test** seeding a mix of sessions, some with plan-mode
  messages and some with Skill tool calls.

- [ ] **Step 2: Implement.** Derive from the `tool_calls` and `messages` tables:

```go
func (db *DB) computeAdoption(ctx context.Context, s *SessionStats, f StatsFilter, rows []sessionStatsRow) error {
    claudeRows := filterByAgent(rows, "claude")
    if len(claudeRows) == 0 {
        return nil
    }
    // plan_mode_rate: fraction of sessions that contain at least one
    // plan-mode tool call. Check tool_calls.tool_name or a known
    // payload marker. Inspect data/schema to confirm detection rule.
    planModeSessionIDs, err := db.sessionIDsWithToolName(ctx, f, "ExitPlanMode")
    if err != nil { return err }
    subagentCounts, err := db.sessionIDsWithToolName(ctx, f, "Task")
    if err != nil { return err }
    // distinct_skills: count unique args.skill values from tool_calls
    // where tool_name = 'Skill' (or SlashCommand, whichever the code
    // records).
    distinctSkills, err := db.distinctSkillInvocations(ctx, f)
    if err != nil { return err }

    adopt := &StatsAdoption{
        ClaudeOnly:          true,
        PlanModeRate:        float64(len(planModeSessionIDs)) / float64(len(claudeRows)),
        SubagentsPerSession: float64(totalSubagents(subagentCounts)) / float64(len(claudeRows)),
        DistinctSkills:      distinctSkills,
    }
    s.Adoption = adopt
    return nil
}
```

If the `Skill` tool's argument shape is unclear after inspecting `tool_calls`
rows locally, default to omitting `distinct_skills` (set to 0 and log a TODO
comment). Spec allows dropping this field if data isn't cleanly available.

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db(stats): implement adoption (plan_mode_rate, subagents, distinct_skills)"
```

______________________________________________________________________

## Phase 3 — Git integration

Git integration lives in a new `internal/db/git` subpackage. Three files + one
SQL migration for the TTL cache.

### Task 14: Scaffold `internal/db/git/` package with repo discovery

**Files:**

- Create: `internal/db/git/repos.go`

- Create: `internal/db/git/repos_test.go`

- [ ] **Step 1: Write failing test** that creates a tmp dir tree with a `.git/`
  at `tmp/repoA/` and sessions in `tmp/repoA/subdir` and `tmp/outside/`, then
  asserts `DiscoverRepos` returns `{tmp/repoA}`.

- [ ] **Step 2: Implement.**

```go
// internal/db/git/repos.go
package git

import (
    "os"
    "path/filepath"
)

// DiscoverRepos walks up from each cwd looking for a .git directory.
// Returns a deduplicated list of repo toplevels that exist.
func DiscoverRepos(cwds []string) []string {
    seen := map[string]struct{}{}
    out := []string{}
    for _, cwd := range cwds {
        root := findRepoRoot(cwd)
        if root == "" { continue }
        if _, ok := seen[root]; ok { continue }
        seen[root] = struct{}{}
        out = append(out, root)
    }
    return out
}

func findRepoRoot(start string) string {
    dir := start
    for {
        if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
            return dir
        }
        parent := filepath.Dir(dir)
        if parent == dir {
            return ""
        }
        dir = parent
    }
}
```

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git add internal/db/git/
git commit -m "db/git: discover repo toplevels from session cwds"
```

______________________________________________________________________

### Task 15: Implement `git log --numstat` parsing

**Files:**

- Create: `internal/db/git/log.go`, `internal/db/git/log_test.go`

- [ ] **Step 1: Failing test** with a fixture repo (created inside `t.TempDir()`
  using `git init`, a sequence of commits with known file/LOC changes by a known
  author within a window). Assert `AggregateLog(repo, author, since, until)`
  returns `{Commits: N, LOCAdded: M, LOCRemoved: K, FilesChanged: F}`.

- [ ] **Step 2: Implement using `os/exec` +
  `git log --numstat --since ... --until ... --author <email>`.**

Parse `git log` output format: each commit is `commit <sha>` header + `Author:`
line + blank + subject + blank + numstat lines
(`added<TAB>removed<TAB>filename`). For binary files numstat emits
`-\t-\t<filename>` which should be skipped for LOC but counted for
files_changed.

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db/git: parse 'git log --numstat' into author-filtered totals"
```

______________________________________________________________________

### Task 16: Implement `gh pr list` aggregation

**Files:**

- Create: `internal/db/git/pr.go`, `internal/db/git/pr_test.go`

- [ ] **Step 1: Write test** using a mock `gh` executable (a shell script in
  `t.TempDir()` that echoes a canned JSON response). Assert
  `AggregatePRs(repo, since, until, ghToken)` returns the expected
  `{Opened, Merged}` counts.

- [ ] **Step 2: Implement.**

Two queries per repo:

```go
// prs_opened: PRs created in window
args1 := []string{"pr", "list", "--state=all", "--author=@me",
                  "--search=created:>=" + since + ".." + until,
                  "--json", "state", "--limit", "500"}
// prs_merged: PRs merged in window (regardless of when created)
args2 := []string{"pr", "list", "--state=merged", "--author=@me",
                  "--search=merged:>=" + since + ".." + until,
                  "--json", "state", "--limit", "500"}
```

Pass `GH_TOKEN=...` in the exec env. Return `nil, nil` when `ghToken == ""` (let
caller distinguish "unknown" from zero).

- [ ] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db/git: aggregate prs_opened/prs_merged via gh CLI"
```

______________________________________________________________________

### Task 17: TTL cache for git results

**Files:**

- Modify: `internal/db/schema.sql`

- Create: `internal/db/git/cache.go`, `internal/db/git/cache_test.go`

- [ ] **Step 1: Add table to `schema.sql`.**

```sql
CREATE TABLE IF NOT EXISTS git_cache (
    cache_key   TEXT PRIMARY KEY,          -- sha256(repo|author|since|until|kind)
    kind        TEXT NOT NULL,             -- 'log' | 'pr'
    payload     TEXT NOT NULL,             -- JSON-encoded result
    computed_at TEXT NOT NULL              -- RFC3339
);
```

- [ ] **Step 2: Write failing test** for
  `CachedOrCompute(ctx, key, ttl, compute)`: first call invokes compute; second
  call within TTL returns cached; call past TTL invokes compute again.

- [ ] **Step 3: Implement `cache.go`.**

```go
type Cache struct { db *sql.DB }
func NewCache(db *sql.DB) *Cache { return &Cache{db: db} }

func (c *Cache) GetOrCompute(ctx context.Context, key, kind string, ttl time.Duration, compute func() ([]byte, error)) ([]byte, error) {
    // SELECT payload, computed_at FROM git_cache WHERE cache_key = ?
    // If found and within TTL, return. Else run compute, INSERT OR REPLACE, return.
    ...
}
```

- [ ] **Step 4: Wire cache into log/pr aggregation functions.**

Modify Task 15 & 16 implementations so they call the cache wrapper. Cache key
format: `sha256("log|"+repo+"|"+author+"|"+since+"|"+until)` (similar for pr).

- [ ] **Step 5: Run all git tests → PASS. Commit.**

```bash
git commit -am "db/git: TTL-cache git log and gh pr results in SQLite"
```

______________________________________________________________________

### Task 18: Wire git integration into `outcome_stats` section

**Files:**

- Modify: `internal/db/session_stats.go`, `internal/db/session_stats_test.go`

- [x] **Step 1: Failing test** with a fixture git repo and canned session cwds;
  assert `outcome_stats.commits`, `loc_added`, etc.

- [x] **Step 2: Implement.**

```go
func (db *DB) computeOutcomeStats(ctx context.Context, s *SessionStats, f StatsFilter, rows []sessionStatsRow) error {
    cwds := make([]string, 0, len(rows))
    for _, r := range rows {
        if r.cwd != "" {
            cwds = append(cwds, r.cwd)
        }
    }
    repos := git.DiscoverRepos(cwds)
    if len(repos) == 0 {
        return nil // leave OutcomeStats nil
    }
    cache := git.NewCache(db.getWriter())
    os := &StatsOutcomeStats{}
    for _, repo := range repos {
        email := git.AuthorEmail(repo) // per-repo first, fall back to global
        if email == "" { continue }
        logRes, err := git.AggregateLogCached(ctx, cache, repo, email, f.Since, f.Until, time.Hour)
        if err != nil { continue }
        os.ReposActive++
        os.Commits      += logRes.Commits
        os.LOCAdded     += logRes.LOCAdded
        os.LOCRemoved   += logRes.LOCRemoved
        os.FilesChanged += logRes.FilesChanged

        if f.GHToken != "" {
            prRes, err := git.AggregatePRsCached(ctx, cache, repo, f.Since, f.Until, f.GHToken, time.Hour)
            if err == nil {
                addPtr(&os.PRsOpened, prRes.Opened)
                addPtr(&os.PRsMerged, prRes.Merged)
            }
        }
    }
    s.OutcomeStats = os
    return nil
}
```

Add `cwd` to `sessionStatsRow` / `loadSessionsInWindow`.

- [x] **Step 3: Run → PASS. Commit.**

```bash
git commit -am "db(stats): aggregate outcome_stats from repo-discovered git data"
```

______________________________________________________________________

## Phase 4 — CLI output, integration, documentation

### Task 19: Implement human-readable CLI output

**Files:**

- Modify: `cmd/agentsview/session_stats.go`

- [ ] **Step 1: Replace the stub `printSessionStatsHuman` with a real
  formatter.**

Target format — tables / simple text. Example shape:

```
Session window: 2026-03-21T00:00:00Z → 2026-04-18T00:00:00Z (28 days)
Agent filter:   all
Timezone:       America/New_York

Totals
  Sessions:              11,905 (human 322, automation 11,583)
  Messages:              109,324  (user 3,012)

Archetypes
  Automation  11,583
  Quick          125
  Standard       101
  Deep            79
  Marathon        17
  Primary: automation  (primary_human: quick)

Session shape (p50/p90 from merged human buckets)
  Duration (min):        p50=22  mean=14.7
  User messages:         p50=14  mean=11.2
  Peak context (tokens): p50=48k (ns=0 null)
  Tools per turn:        p50=1   mean=2.3

... (similar for velocity, tool/model mix, agent portfolio, temporal summary, outcomes) ...
```

- [ ] **Step 2: Manual smoke check.**

```bash
go run ./cmd/agentsview session stats --since 7d
go run ./cmd/agentsview session stats --format json --since 7d | jq .schema_version
```

Expected: first gives the formatted table; second prints `1`.

- [ ] **Step 3: Commit.**

```bash
git commit -am "cmd: human-readable formatter for 'session stats'"
```

______________________________________________________________________

### Task 20: Golden-file integration test + documentation

**Files:**

- Create: `cmd/agentsview/testdata/session_stats_golden.json`

- Modify: `cmd/agentsview/session_stats_test.go`

- Modify: `README.md` or `cmd/agentsview/README.md` — document the subcommand

- Modify: `CHANGELOG.md` (if present)

- [ ] **Step 1: Build a deterministic fixture DB.**

Use `cmd/testfixture` (already exists for E2E tests) to seed a small session set
with fixed timestamps. Record the expected JSON output by running
`agentsview session stats --format json` once, capturing it, and storing as
`testdata/session_stats_golden.json`.

- [ ] **Step 2: Write the integration test.**

```go
func TestSessionStatsGolden(t *testing.T) {
    dbPath := buildFixtureDB(t)
    cmd := exec.Command("go", "run", "./cmd/agentsview", "session", "stats",
                        "--format", "json", "--since", "30d")
    cmd.Env = append(os.Environ(), "AGENTSVIEW_DB="+dbPath)
    out, err := cmd.Output()
    if err != nil { t.Fatal(err) }

    var got, want map[string]any
    if err := json.Unmarshal(out, &got); err != nil { t.Fatal(err) }
    golden, err := os.ReadFile("testdata/session_stats_golden.json")
    if err != nil { t.Fatal(err) }
    if err := json.Unmarshal(golden, &want); err != nil { t.Fatal(err) }

    // Zero out generated_at for deterministic comparison.
    delete(got, "generated_at")
    delete(want, "generated_at")

    if !reflect.DeepEqual(got, want) {
        // Diff output helpful here — consider go-cmp for prettier errors.
        t.Errorf("JSON mismatch: see diff")
    }
}
```

- [ ] **Step 3: Document the command.** Add a short section to `README.md`
  showing a sample invocation and linking to the spec.

- [ ] **Step 4: Run full test suite.**

```bash
go test ./...
```

Expected: all green.

- [ ] **Step 5: Commit.**

```bash
git add cmd/agentsview/testdata/session_stats_golden.json cmd/agentsview/session_stats_test.go README.md
git commit -m "cmd(stats): golden-file integration test + docs"
```

______________________________________________________________________

## Post-implementation checklist

- [ ] All tests pass: `go test ./...`
- [ ] Vet clean: `go vet ./...`
- [ ] Build binary:
  `go build ./cmd/agentsview && ./agentsview session stats --format json --since 7d | jq keys | head`
- [ ] Run against real local data: `agentsview session stats --since 28d` —
  eyeball output
- [ ] JSON against real data:
  `agentsview session stats --format json --since 28d | jq . > /tmp/stats.json && jq 'keys' /tmp/stats.json`
- [ ] Confirm every schema field in the spec has a non-null value for a user
  with real data
- [ ] No new dependencies in `go.mod` outside stdlib + the existing set (this
  plan should need none)

## Open items to iterate on after initial merge

Per the spec's "Open questions" section — expect these to come up during local
testing:

1. **Skill-usage extraction** — confirm how `Skill` tool invocations are
   recorded in `tool_calls`. If the skill name isn't cleanly available from a
   structured field, leave `distinct_skills: 0` and fix in a follow-up.
1. **Bucket boundary tuning** — after running against real data, if histograms
   look sparse or lopsided, flag it to the user for a pre-PR tuning decision.
   Bucket changes inside schema_version 1 are allowed before the tkmx-server PR
   locks them in.
1. **Cache TTL** — 1 hour default. If git-log becomes a hot-path annoyance
   during testing, bump to 24 hours or add invalidation on HEAD change.
