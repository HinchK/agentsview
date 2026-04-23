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

Add two helpers near the existing `extractCommandText`:

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
`:693-706`) into a single helper:

```go
// firstMessageAndUserCount returns the preview string and the total
// number of real (non-system) user turns. The preview skips known
// Claude Code command envelopes like /clear and /effort so sessions
// that begin with a command still show a meaningful preview.
func firstMessageAndUserCount(messages []Message) (string, int) {
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

## Out of scope

- iFlow and other agent parsers. iFlow uses the same envelope format but
  `/clear` and `/effort` are not iFlow commands; applying the skip there is
  speculative.
- User-configurable skip list. Hardcoded list is trivial to expand.
- Frontend changes. `first_message` remains the single source of truth.
- Backfill code. `dataVersion` bump already triggers re-parse.
