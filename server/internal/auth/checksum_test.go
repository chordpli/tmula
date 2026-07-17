package auth

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestSourceChecksumExcludesSecrets is the secret-free guard: a shared-source
// checksum is computed over subjects + order + count ONLY, so two pools with
// IDENTICAL subjects in the same order but DIFFERENT secrets hash the SAME. This
// proves no secret bytes enter the digest — a naive Go struct hash would include
// the secret even though it carries json:"-" (struct hashing does not honor it).
func TestSourceChecksumExcludesSecrets(t *testing.T) {
	t.Parallel()

	a := []domain.Credential{
		{Subject: "u0", Secret: "secret-a-0"},
		{Subject: "u1", Secret: "secret-a-1"},
		{Subject: "u2", Secret: "secret-a-2"},
	}
	b := []domain.Credential{
		{Subject: "u0", Secret: "TOTALLY-DIFFERENT-0"},
		{Subject: "u1", Secret: "TOTALLY-DIFFERENT-1"},
		{Subject: "u2", Secret: "TOTALLY-DIFFERENT-2"},
	}

	if SourceChecksum(a) != SourceChecksum(b) {
		t.Fatal("identical subjects+order with different secrets must produce the SAME checksum (secrets must not enter the digest)")
	}
}

// TestSourceChecksumDistinguishesSubjectsOrderAndCount pins that the checksum is
// a real guard: a different subject, a different ORDER, or a different COUNT each
// changes the digest, so two workers reading divergent pools cannot pass the
// shared-source check by accident.
func TestSourceChecksumDistinguishesSubjectsOrderAndCount(t *testing.T) {
	t.Parallel()

	base := []domain.Credential{
		{Subject: "u0", Secret: "s0"},
		{Subject: "u1", Secret: "s1"},
		{Subject: "u2", Secret: "s2"},
	}
	want := SourceChecksum(base)

	t.Run("different subject changes the digest", func(t *testing.T) {
		t.Parallel()
		diff := []domain.Credential{{Subject: "u0", Secret: "s0"}, {Subject: "X", Secret: "s1"}, {Subject: "u2", Secret: "s2"}}
		if SourceChecksum(diff) == want {
			t.Error("a different subject must change the checksum")
		}
	})

	t.Run("different order changes the digest", func(t *testing.T) {
		t.Parallel()
		reordered := []domain.Credential{{Subject: "u1", Secret: "s1"}, {Subject: "u0", Secret: "s0"}, {Subject: "u2", Secret: "s2"}}
		if SourceChecksum(reordered) == want {
			t.Error("a different order must change the checksum (assignment is order-sensitive)")
		}
	})

	t.Run("different count changes the digest", func(t *testing.T) {
		t.Parallel()
		shorter := []domain.Credential{{Subject: "u0", Secret: "s0"}, {Subject: "u1", Secret: "s1"}}
		if SourceChecksum(shorter) == want {
			t.Error("a different count must change the checksum (wrap-around depends on N)")
		}
	})
}

// TestSourceChecksumSubjectBoundary pins that the digest cannot be spoofed by
// shifting bytes across the subject boundary: "ab","c" and "a","bc" must differ.
func TestSourceChecksumSubjectBoundary(t *testing.T) {
	t.Parallel()
	x := []domain.Credential{{Subject: "ab"}, {Subject: "c"}}
	y := []domain.Credential{{Subject: "a"}, {Subject: "bc"}}
	if SourceChecksum(x) == SourceChecksum(y) {
		t.Error("subject boundaries must be encoded so concatenation cannot collide")
	}
}
