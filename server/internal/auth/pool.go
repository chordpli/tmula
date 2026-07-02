package auth

import (
	"context"
	"fmt"

	"github.com/chordpli/tmula/server/internal/domain"
)

// PoolProvider hands out pre-supplied credentials, one per virtual user,
// wrapping around if there are more users than credentials.
type PoolProvider struct {
	entries []domain.Credential
}

// NewPoolProvider builds a pool provider from pre-supplied credentials.
func NewPoolProvider(entries []domain.Credential) (*PoolProvider, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("auth: pool provider needs at least one credential")
	}
	return &PoolProvider{entries: entries}, nil
}

// Acquire returns the credential assigned to userIndex.
func (p *PoolProvider) Acquire(_ context.Context, userIndex int) (domain.Credential, error) {
	if userIndex < 0 {
		return domain.Credential{}, fmt.Errorf("auth: negative user index %d", userIndex)
	}
	return p.entries[userIndex%len(p.entries)], nil
}
