# Skip `/clear` and `/effort` when computing Claude `first_message`

## Problem

The left sidebar shows `session.first_message` as each session's preview text.
For Claude Code sessions where the user's first action is `/clear` or `/effort`,
the preview reads as that command instead of something descriptive. Users who
use these commands often end up with sidebars full of `/clear` and `/effort max`
previews.

`first_message` is computed once during parsing in each agent's parser and
stored on the `sessions` row. The Claude parser normalizes
`<command-name>/X</command-name>` envelopes into human-readable text like
`/clear` or `/effort max` (via `extractCommandText` in
`internal/parser/claude.go`), so these land in `first_message` verbatim.

## Goal

When the Claude parser computes `first_message`, skip user messages whose
normalized content is the `/clear` or `/effort` command, cascading through any
number of leading skipped commands until a real message is found. Fall back to
an empty string if every user message is skipped (same as today for sessions
with no user messages).

`user_message_count` is unchanged — skipped commands still count as user turns.

## Scope

- Claude parser only (`internal/parser/claude.go`). These commands are specific
  to Claude Code; other agents do not see them.
- Skip list is hardcoded: `/clear`, `/effort`. A future change can expand it.
- Match on a word boundary: the trimmed content must equal the command exactly
  or be followed by whitespace. `/clearcache` and `/effortless` do not match.

## Implementation

### `internal/parser/claude.go`

Add two helpers near the existing `extractCommandText`. The helper uses
`unicode/utf8` and `unicode` (already imported); `strings` is already imported.

```go
// previewSkippedCommands lists commands that should not be used as
// a session's first_message preview. Messages matching these are
// skipped over so the sidebar shows the next real message instead.
var previewSkippedCommands = []string{"/clear", "/effort"}

// isSkippablePreviewCommand returns true when content is exactly
// a known command (possibly with arguments), for the purpose of
// skipping it when computing first_message. Match is word-boundary:
// the command must equal the trimmed content or be followed by a
// whitespace rune, so "/clearcache" does not match "/clear".
func isSkippablePreviewCommand(content string) bool {
    trimmed := strings.TrimSpace(content)
    for _, cmd := range previewSkippedCommands {
        if !strings.HasPrefix(trimmed, cmd) {
            continue
        }
        if len(trimmed) == len(cmd) {
            return true
        }
        r, _ := utf8.DecodeRuneInString(trimmed[len(cmd):])
        if unicode.IsSpace(r) {
            return true
        }
    }
    return false
}
```

Extract the duplicated first-message/user-count loops (`:519-538` and
`:693-706`) into a single helper. The parser operates on `[]ParsedMessage`
returned by `extractMessages`, not `Message`.

```go
// firstMessageAndUserCount returns the preview string and the total
// number of real (non-system) user turns. The preview skips known
// Claude Code command envelopes like /clear and /effort so sessions
// that begin with a command still show a meaningful preview.
func firstMessageAndUserCount(messages []ParsedMessage) (string, int) {
    firstMsg := ""
    userCount := 0
    for _, m := range messages {
        if m.IsSystem {
            continue
        }
        if m.Role != RoleUser || m.Content == "" {
            continue
        }
        userCount++
        if firstMsg == "" && !isSkippablePreviewCommand(m.Content) {
            firstMsg = truncate(
                strings.ReplaceAll(m.Content, "\n", " "), 300,
            )
        }
    }
    return firstMsg, userCount
}
```

Replace the two inline loops with calls to `firstMessageAndUserCount`.

### `internal/db/db.go`

Bump `dataVersion` from 17 to 18 with a comment block explaining that the Claude
parser now skips `/clear` and `/effort` when computing `first_message`. Existing
DBs trigger the existing non-destructive re-sync path (mtime reset + skip cache
clear), so sessions are re-parsed with the new logic on next start.

### Incremental sync path

The incremental path (`writeIncremental` in `internal/sync/engine.go:3183` and
`UpdateSessionIncremental` in `internal/db/sessions.go:1085`) appends messages
without overwriting `first_message`. Without changes here, a session can sync
once with only `/clear` as its opening user turn — producing an empty
`first_message` under the new skip logic — and later incremental syncs that
append the real prompt will leave `first_message` empty forever.

Fix by forcing a full parse at the incremental entry point when the stored
session already has user turns but no preview string:

**`internal/db/sessions.go`** — extend `IncrementalInfo` with
`FirstMessage string` and update `GetSessionForIncremental` to select
`first_message` into it. The column is nullable, so read into a `sql.NullString`
and copy the value (or empty) into the field.

**`internal/sync/engine.go`** — `tryIncrementalJSONL` already receives
`agent parser.AgentType`. In that function, alongside the existing data-version
and file-identity fall-through checks (currently around `:2186-2214`), add:

```go
// Claude-only: if the stored preview is empty despite having user
// turns, the Claude parser skipped every user message so far
// (e.g. a session that opens with /clear). A full parse gives the
// newly-appended real user message a chance to become first_message.
//
// Other agents (e.g. Codex) can legitimately have UserMsgCount > 0
// with an empty first_message — codex inserts orphan subagent
// notifications as Role=user messages via insertMessage, which
// bypasses the firstMessage setter — so the fall-through must be
// gated on agent type.
if agent == parser.AgentClaude &&
    inc.FirstMessage == "" && inc.UserMsgCount > 0 {
    return processResult{}, false
}
```

The `currentSize <= inc.FileSize` early return (`:2192`) already guarantees we
only fall through when the file has new bytes, bounding the extra full-parse
cost.

## Tests

New cases in `internal/parser/claude_parser_test.go`:

1. **Unit test for `isSkippablePreviewCommand`** (table-driven):

   - Positive: `/clear`, `/effort`, `/clear ` (trailing space), `/clear foo`,
     `/effort max`, `  /clear  ` (surrounding whitespace).
   - Negative: empty string, `/clearcache`, `/effortless`, `/cleareffort`,
     `/unrelated`, `hello /clear`, `/clear-xyz`.

1. **Parser E2E tests** using JSONL fixtures:

   - First user message is `/clear` envelope, second is real text → assert
     `first_message` equals the real text, `user_message_count` equals 2.
   - First two user messages are `/effort max` then `/clear`, third is real →
     assert `first_message` equals the third, `user_message_count` equals 3.
   - All user messages are skipped commands → assert `first_message` is empty,
     `user_message_count` equals the number of command messages.
   - Control: first user message is `/roborev-fix 450` (a non-skipped command) →
     assert `first_message` equals `/roborev-fix 450`, confirming we haven't
     broadened the skip list.

1. **Incremental sync test** in `internal/sync/engine_integration_test.go` (or
   the parser equivalent): write a session file containing only a `/clear` user
   envelope, sync it (first_message stored as empty). Append a real user message
   to the same file, sync again, and assert that the session's `first_message`
   is now the real message. This exercises the full-parse fall-through added to
   `tryIncrementalJSONL`.

## Out of scope

- iFlow and other agent parsers. iFlow uses the same envelope format but
  `/clear` and `/effort` are not iFlow commands; applying the skip there is
  speculative.
- User-configurable skip list. Hardcoded list is trivial to expand.
- Frontend changes. `first_message` remains the single source of truth.
- Backfill code. `dataVersion` bump already triggers re-parse.
