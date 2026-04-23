package db

import (
	"testing"
)

func TestClassifierHashStable(t *testing.T) {
	t.Cleanup(func() { SetUserAutomationPrefixes(nil) })
	SetUserAutomationPrefixes([]string{"foo", "bar"})
	a := ClassifierHash()
	b := ClassifierHash()
	if a != b {
		t.Errorf("hash unstable: %s vs %s", a, b)
	}
}

func TestClassifierHashChangesWithUserPrefixes(t *testing.T) {
	t.Cleanup(func() { SetUserAutomationPrefixes(nil) })
	SetUserAutomationPrefixes(nil)
	base := ClassifierHash()
	SetUserAutomationPrefixes([]string{"You are analyzing an essay"})
	with := ClassifierHash()
	if base == with {
		t.Errorf("hash did not change when user prefixes changed: %s", base)
	}
}

func TestClassifierHashOrderIndependent(t *testing.T) {
	t.Cleanup(func() { SetUserAutomationPrefixes(nil) })
	SetUserAutomationPrefixes([]string{"alpha", "beta", "gamma"})
	a := ClassifierHash()
	SetUserAutomationPrefixes([]string{"gamma", "alpha", "beta"})
	b := ClassifierHash()
	if a != b {
		t.Errorf("hash not order-independent: %s vs %s", a, b)
	}
}

// TestClassifierHashTagSeparation guards against the case
// where two different categorizations produce the same hash
// because the tag prefix was dropped from the encoding.
func TestClassifierHashTagSeparation(t *testing.T) {
	t.Cleanup(func() { SetUserAutomationPrefixes(nil) })
	SetUserAutomationPrefixes([]string{"Warmup"})
	got := ClassifierHash()
	SetUserAutomationPrefixes(nil)
	bareBuiltins := ClassifierHash()
	if got == bareBuiltins {
		t.Errorf(
			"user prefix 'Warmup' collided with built-in exact-match 'Warmup': %s",
			got,
		)
	}
}

// TestClassifierHashCurrentAlgoVersion is a forced-bump
// guard: it pins the algorithm version at construction time.
// If a future change to the matching logic forgets to bump
// classifierAlgorithmVersion, this test still passes (false
// negative) — but if someone bumps the version intentionally
// the test must be updated to match. The check exists to
// surface accidental version-constant edits during review.
func TestClassifierHashCurrentAlgoVersion(t *testing.T) {
	if classifierAlgorithmVersion != 2 {
		t.Fatalf(
			"classifierAlgorithmVersion changed to %d; "+
				"update this test and confirm matching "+
				"semantics actually changed (not just "+
				"pattern edits, which the hash already "+
				"detects)",
			classifierAlgorithmVersion,
		)
	}
}
