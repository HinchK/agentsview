package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// skipIfNoGit lets CI environments without git on PATH pass cleanly instead
// of failing the package.
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available on PATH: %v", err)
	}
}

// gitRun executes a git subcommand inside repo and fails the test on error.
// Env overrides let callers control author identity per commit.
func gitRun(t *testing.T, repo string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initRepo creates a fresh repo at t.TempDir() with a deterministic default
// identity. Individual commits override the author via GIT_AUTHOR_* envs.
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, nil, "init", "-q", "-b", "main")
	gitRun(t, repo, nil, "config", "user.email", "test@example.com")
	gitRun(t, repo, nil, "config", "user.name", "Test User")
	// Disable signing so tests don't hang on a GPG/passphrase prompt
	// when the user's global config has signing enabled.
	gitRun(t, repo, nil, "config", "commit.gpgsign", "false")
	return repo
}

// writeFile writes content under repo/relpath, creating parents as needed.
func writeFile(t *testing.T, repo, relpath string, content []byte) {
	t.Helper()
	p := filepath.Join(repo, relpath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// commitAs stages all changes and commits with an explicit author identity.
func commitAs(t *testing.T, repo, email, name, message string) {
	t.Helper()
	env := []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
	gitRun(t, repo, nil, "add", "-A")
	gitRun(t, repo, env, "commit", "-q", "-m", message)
}

func TestAggregateLog_CountsCommitsLOCAndFiles(t *testing.T) {
	skipIfNoGit(t)
	repo := initRepo(t)

	// Commit 1 (test@example.com): add a.txt with 3 lines.
	writeFile(t, repo, "a.txt", []byte("a1\na2\na3\n"))
	commitAs(t, repo, "test@example.com", "Test User", "c1: add a.txt")

	// Commit 2 (test@example.com): modify a.txt (+3 -1) and add b.txt (+5).
	writeFile(t, repo, "a.txt", []byte("a1\na2-changed\na3\na4\na5\n"))
	writeFile(t, repo, "b.txt", []byte("b1\nb2\nb3\nb4\nb5\n"))
	commitAs(t, repo, "test@example.com", "Test User", "c2: edit a.txt, add b.txt")

	// Commit 3 (test@example.com): add a binary file (null byte triggers git's
	// binary detection, so numstat emits "-\t-\t...").
	writeFile(t, repo, "binary.dat", []byte{0x00, 0x01, 0x02, 0x03, 0xff})
	commitAs(t, repo, "test@example.com", "Test User", "c3: add binary")

	// Non-matching commit (other@example.com): should be excluded.
	writeFile(t, repo, "a.txt", []byte("a1\na2-changed\na3\na4\na5\nfrom-other\n"))
	commitAs(t, repo, "other@example.com", "Other User", "c4: other author")

	// Use a wide window — all commits are "now".
	got, err := AggregateLog(
		context.Background(),
		repo, "test@example.com",
		"1970-01-01T00:00:00Z", "2099-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("AggregateLog: %v", err)
	}

	// Expected totals for test@example.com across the three commits. Values
	// reflect git's diff for each commit; verified manually via
	// `git log --numstat --format=%H` against an identical fixture.
	//   c1 a.txt      +3 -0
	//   c2 a.txt      +3 -1  (line 2 replaced + two trailing lines added)
	//   c2 b.txt      +5 -0
	//   c3 binary.dat  0  0  (binary: LOC skipped, file still counted)
	want := LogResult{
		Commits:      3,
		LOCAdded:     11,
		LOCRemoved:   1,
		FilesChanged: 4,
	}
	if got != want {
		t.Fatalf("AggregateLog = %+v, want %+v", got, want)
	}
}

func TestAggregateLog_EmptyWindowReturnsZero(t *testing.T) {
	skipIfNoGit(t)
	repo := initRepo(t)

	writeFile(t, repo, "a.txt", []byte("hello\n"))
	commitAs(t, repo, "test@example.com", "Test User", "c1")

	// Window in the distant past — no commits fall inside.
	got, err := AggregateLog(
		context.Background(),
		repo, "test@example.com",
		"1970-01-01T00:00:00Z", "1970-01-02T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("AggregateLog: %v", err)
	}
	if got != (LogResult{}) {
		t.Fatalf("AggregateLog = %+v, want zero value", got)
	}
}

func TestAggregateLog_UnknownAuthorReturnsZero(t *testing.T) {
	skipIfNoGit(t)
	repo := initRepo(t)

	writeFile(t, repo, "a.txt", []byte("hello\n"))
	commitAs(t, repo, "test@example.com", "Test User", "c1")

	got, err := AggregateLog(
		context.Background(),
		repo, "nobody@example.invalid",
		"1970-01-01T00:00:00Z", "2099-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("AggregateLog: %v", err)
	}
	if got != (LogResult{}) {
		t.Fatalf("AggregateLog = %+v, want zero value", got)
	}
}

func TestAggregateLog_BadRepoReturnsError(t *testing.T) {
	skipIfNoGit(t)
	// A temp directory that is NOT a git repo.
	notARepo := t.TempDir()

	_, err := AggregateLog(
		context.Background(),
		notARepo, "test@example.com",
		"1970-01-01T00:00:00Z", "2099-01-01T00:00:00Z",
	)
	if err == nil {
		t.Fatalf("AggregateLog on non-repo: expected error, got nil")
	}
}

func TestAuthorEmail_LocalConfig(t *testing.T) {
	skipIfNoGit(t)
	repo := t.TempDir()
	gitRun(t, repo, nil, "init", "-q", "-b", "main")
	gitRun(t, repo, nil, "config", "user.email", "local@example.com")

	got := AuthorEmail(repo)
	if got != "local@example.com" {
		t.Fatalf("AuthorEmail = %q, want %q", got, "local@example.com")
	}
}

func TestAuthorEmail_FallsBackToGlobal(t *testing.T) {
	skipIfNoGit(t)
	// Isolate HOME + XDG_CONFIG_HOME so the "global" config is a scratch file
	// we control; the local repo intentionally has no user.email set.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Some git builds also consult GIT_CONFIG_GLOBAL; point it at a known path.
	globalCfg := filepath.Join(home, ".gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)

	// Seed the global config with our expected email.
	setGlobal := exec.Command("git", "config", "--global", "user.email", "global@example.com")
	setGlobal.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL="+globalCfg,
	)
	if out, err := setGlobal.CombinedOutput(); err != nil {
		t.Fatalf("seed global config: %v\n%s", err, out)
	}

	repo := t.TempDir()
	// Init with no local user.email — `AuthorEmail` must fall through to global.
	initCmd := exec.Command("git", "init", "-q", "-b", "main")
	initCmd.Dir = repo
	initCmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL="+globalCfg,
	)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	got := AuthorEmail(repo)
	if got != "global@example.com" {
		t.Fatalf("AuthorEmail (global fallback) = %q, want %q", got, "global@example.com")
	}
}

func TestParseNumstat_SkipsBinaryLOCButCountsFile(t *testing.T) {
	// Unit test of the pure parser, independent of git exec.
	input := []byte(strings.Join([]string{
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		"",
		"3\t0\ta.txt",
		"-\t-\tbinary.dat",
		"",
		"b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0",
		"",
		"2\t1\ta.txt",
		"5\t0\tb.txt",
		"",
	}, "\n"))

	got := parseNumstat(input)
	want := LogResult{
		Commits:      2,
		LOCAdded:     10,
		LOCRemoved:   1,
		FilesChanged: 4,
	}
	if got != want {
		t.Fatalf("parseNumstat = %+v, want %+v", got, want)
	}
}
