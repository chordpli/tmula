package auth

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/chordpli/tmula/server/internal/domain"
)

// SourceChecksum digests a loaded credential pool over its SUBJECTS, their ORDER
// and the COUNT — never the secrets. It is the secret-free guard a control plane
// uses to assert that every distributed worker resolved the SAME shared source in
// the SAME order: the credential assignment is a pure function of the global index
// over these entries (PoolProvider.Acquire keys entries[g % count]), so identical
// subjects + order + count means every worker reconstructs the identical provider.
//
// The digest deliberately reads ONLY Subject (a non-sensitive principal id), so no
// secret byte ever enters it — a Go struct hash of a Credential would include the
// Secret even though it carries json:"-" (struct hashing does not honor the tag).
// Each subject is length-prefixed so concatenation cannot collide ("ab"+"c" vs
// "a"+"bc"), and the count is folded in so wrap-around (which depends on N) is
// part of the identity.
func SourceChecksum(entries []domain.Credential) string {
	h := sha256.New()
	var n [8]byte
	// Count first, so a different number of entries (different wrap-around) always
	// changes the digest even if a prefix of subjects matches.
	binary.BigEndian.PutUint64(n[:], uint64(len(entries)))
	_, _ = h.Write(n[:])
	for _, c := range entries {
		// Length-prefix the subject so byte shifts across the boundary cannot
		// produce the same stream from different subject lists.
		binary.BigEndian.PutUint64(n[:], uint64(len(c.Subject)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(c.Subject))
	}
	return hex.EncodeToString(h.Sum(nil))
}
