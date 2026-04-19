package git

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// LogResult aggregates author-filtered counts from `git log --numstat` output.
type LogResult struct {
	Commits      int
	LOCAdded     int
	LOCRemoved   int
	FilesChanged int
}

// AggregateLog runs `git log --numstat` filtered by author and window and
// returns total commits, lines added, lines removed, and files changed.
//
// The since/until timestamps should be RFC3339 strings; git accepts them
// directly via `--since`/`--until`. LOC counts from binary files (which
// numstat represents as `-\t-\t<path>`) are skipped, but the file still
// counts toward FilesChanged.
//
// If the window is empty or the author matches nothing, a zero-valued
// LogResult is returned with no error. Exec failures (bad repo path, git
// missing from PATH) are surfaced as errors.
func AggregateLog(
	ctx context.Context, repo, authorEmail, since, until string,
) (LogResult, error) {
	cmd := exec.CommandContext(
		ctx, "git", "log",
		"--numstat",
		"--format=%H",
		"--since="+since,
		"--until="+until,
		"--author="+authorEmail,
	)
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return LogResult{}, fmt.Errorf("git log in %s: %w", repo, err)
		}
		return LogResult{}, fmt.Errorf(
			"git log in %s: %w: %s", repo, err, msg,
		)
	}
	return parseNumstat(out), nil
}

// parseNumstat walks `git log --numstat --format=%H` output and aggregates
// commit/LOC/file totals. The format emits a SHA line, a blank line, then
// zero or more numstat lines per commit:
//
//	<40-hex-sha>
//
//	<added>\t<removed>\t<path>
//	-\t-\t<binary-path>
func parseNumstat(out []byte) LogResult {
	var result LogResult
	scanner := bufio.NewScanner(bytes.NewReader(out))
	// Allow long paths; 1 MiB is generous but keeps a hard ceiling.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if isSHALine(line) {
			result.Commits++
			continue
		}
		added, removed, ok := parseNumstatLine(line)
		if !ok {
			continue
		}
		result.FilesChanged++
		result.LOCAdded += added
		result.LOCRemoved += removed
	}
	return result
}

// isSHALine reports whether s is a 40-character lowercase hex SHA, as emitted
// by `--format=%H`.
func isSHALine(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// parseNumstatLine parses a single `added\tremoved\tpath` numstat row.
// Binary files use "-" for both counts; we treat those as zero LOC but
// still return ok=true so the file is counted.
func parseNumstatLine(line string) (added, removed int, ok bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) < 3 {
		return 0, 0, false
	}
	added, err := parseNumstatCount(parts[0])
	if err != nil {
		return 0, 0, false
	}
	removed, err = parseNumstatCount(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return added, removed, true
}

// parseNumstatCount returns 0 for the binary marker "-" and parses digits
// otherwise. Anything else is a parse error so the caller can reject the line.
func parseNumstatCount(s string) (int, error) {
	if s == "-" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

// AuthorEmail returns `git config user.email` run from inside the repo,
// falling back to the global config. Returns "" if neither is set or git
// is not available.
func AuthorEmail(repo string) string {
	cmd := exec.Command("git", "config", "user.email")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v
		}
	}
	cmd = exec.Command("git", "config", "--global", "user.email")
	out, err = cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
