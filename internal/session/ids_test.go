package session

import (
	"regexp"
	"testing"
)

func TestGenerateTestSessionID(t *testing.T) {
	pat := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	first := GenerateTestSessionID("debug-seed")
	if !pat.MatchString(first) {
		t.Fatalf("GenerateTestSessionID = %q, want ses_<12 hex><14 alphanum>", first)
	}
	if again := GenerateTestSessionID("debug-seed"); again != first {
		t.Fatalf("same input gave %q then %q, want deterministic output", first, again)
	}
	if other := GenerateTestSessionID("another-seed"); other == first {
		t.Fatalf("different inputs both gave %q", first)
	}
}
