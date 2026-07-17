package load

import (
	"sync"

	"github.com/chordpli/tmula/server/internal/domain"
)

// CredentialHolder is a mutable, concurrency-safe box around a virtual user's
// current credential. The login/refresh path (a layer above the runner) seeds a
// holder per user and rotates it on a mid-run 401; runSession reads the live value
// from the holder per step ONLY when a user carries one.
//
// It is an interface so the runtime depends on the seam, not a concrete type:
// tests inject a holder whose Get panics to PROVE the static (Holder==nil) path
// never consults it. The shared (client_credentials) login scope hands the SAME
// holder VALUE (a pointer-backed implementation) to every user, so a single
// refresh updates all sessions — keep the field an interface (or pointer); a
// value-typed holder would silently break sharing.
type CredentialHolder interface {
	// Get returns the current credential (concurrency-safe).
	Get() domain.Credential
	// Set replaces the current credential (concurrency-safe), e.g. after a
	// mid-run refresh re-mints the token.
	Set(domain.Credential)
}

// liveHolder is the production CredentialHolder: a mutex-guarded credential. It is
// always used by pointer so a shared-scope login can hand one *liveHolder to every
// user and a single Set is visible to all of them.
type liveHolder struct {
	mu   sync.Mutex
	cred domain.Credential
}

// NewCredentialHolder builds a holder initialized to cred. The returned value is a
// pointer, so assigning it to several users' Holder fields shares one credential
// box (the shared-login behavior); give each user its own holder for per-user.
func NewCredentialHolder(cred domain.Credential) CredentialHolder {
	return &liveHolder{cred: cred}
}

// Get returns the current credential under the holder's lock.
func (h *liveHolder) Get() domain.Credential {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cred
}

// Set replaces the current credential under the holder's lock.
func (h *liveHolder) Set(cred domain.Credential) {
	h.mu.Lock()
	h.cred = cred
	h.mu.Unlock()
}
