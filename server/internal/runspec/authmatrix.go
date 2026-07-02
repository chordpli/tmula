package runspec

import "github.com/chordpli/tmula/server/internal/domain"

// authmatrix.go is the SINGLE SOURCE OF TRUTH for which authenticated runs may
// distribute (run with workers). validateCredentialPool consults it, and the
// reproduce path (api/reproduce.go sessionUser) relies on the same contract; a
// characterization test freezes the exact behavior so this table cannot drift
// silently. A deliberate change (e.g. relaxing login/mint distribution) is made
// HERE, in the table, with the characterization test updated in lockstep.
//
// D1 SPLIT (the load-bearing distributed-auth contract): a distributed run
// authenticates ONLY from a shared, index-deterministic SourceRef that the worker
// resolves LOCALLY; inline secrets and bootstrap stay rejected with workers.
// Concretely:
//
//   - Source != nil && NO workers  → REJECTED. The in-process/API server must not
//     read a client-chosen path off the wire; the CLI resolves single-node sources
//     into entries at scenariofile.Expand, so a non-distributed spec must carry
//     real entries.
//   - Source (file/env) != nil && workers → ALLOWED, the distributed carve-out:
//     only the reference crosses the wire and each worker loads its own slice and
//     assigns by GLOBAL index, so every worker reconstructs the same provider
//     (PoolProvider.Acquire is a pure function of the global index). No secret is
//     serialized. shardSpecFor copies the ref into ShardSpec.CredentialSource.
//   - Inline Entries != nil && workers → REJECTED. The secrets would serialize
//     into the wire spec.
//   - Bootstrap-signup && NO workers → ALLOWED when it carries a SignupFlow and
//     either a teardown journey OR --keep-accounts (the gating-safety rule). The
//     orchestrator compiles the SignupFlow, prewarms one account per virtual user,
//     and defers teardown. A bootstrap pool with no SignupFlow, or no teardown and
//     no keep-accounts, is REJECTED above.
//   - Bootstrap-signup && workers → REJECTED. A bootstrap pool mints real accounts
//     and has no shared reference to fan out; P4 keeps this rejected (distributed
//     bootstrap is a follow-up). (Domain Validate already forbids a Source on a
//     bootstrap pool.)
//   - Login && workers → REJECTED. A minted login token is a json:"-" secret the
//     worker fan-out cannot resolve. (Domain Validate forbids a Source on a login
//     pool, so login never reaches the carve-out.)
//   - Mint && workers → ALLOWED (P4). A mint pool self-issues a JWT per virtual
//     user by signing locally; the pool carries only the NON-SECRET key REFERENCE
//     (MintSpec.Key; the resolved key is json:"-"), so shipping the MintSpec fans
//     out without a secret on the wire. Each worker resolves the SAME key reference
//     locally and signs deterministically by global index (MintProvider.Acquire is
//     pure per index), exactly like a source pool. Operator contract: the same key
//     must be deployed on every worker; a worker that cannot resolve it fails its
//     shard with a clear runtime error. reproduce rebuilds the mint provider on the
//     master (which also holds the key) via the generic CredentialProvider path.
//
// LOAD-BEARING FOR REPRODUCE FIDELITY: every distributed authenticated run is
// either rejected here or carries a source the workers (and reproduce) resolve by
// the SAME pure Acquire(global index). reproduce.go's sessionUser relies on this:
// a distributed-auth finding replays under the same principal the shard ran as
// because both rebuild the source-backed provider and key it by the global index.
// If this split is ever changed, sessionUser must be updated in lockstep (D4: PR3
// and PR4 land together). See also: CredentialProvider, the sessionUser function
// in reproduce.go, and shardSpecFor in orchestrator.go.

// authCapability describes, per credential strategy, how the run-path validation
// treats a distributed (workers) run. Two strategies fan out today: a source-backed
// CredPool (the carve-out handled directly in validateCredentialPool, since a
// Source only ever rides a CredPool) and CredMint (it ships only a key reference).
// Every other strategy is rejected with workers and carries its own rejection
// message.
type authCapability struct {
	// AllowsWorkers is true when a pool of this strategy MAY run with distributed
	// workers because it fans out only a non-secret reference the worker resolves
	// locally (mint's key reference; a source pool is handled by the earlier
	// carve-out). When true, WorkerRejection is unused.
	AllowsWorkers bool
	// WorkerRejection is the error message emitted when a pool of this strategy runs
	// with distributed workers and carries no distributable reference. It is the
	// exact string the characterization test freezes. Unused when AllowsWorkers.
	WorkerRejection string
}

// authMatrix maps each credential strategy onto its distributed-run capability.
// CredPool's message covers the inline-entries case (a source-backed pool takes
// the carve-out before this table is consulted). login shares the same generic
// inline message — a minted token is an inline secret the fan-out cannot resolve.
// mint ALLOWS workers (it ships only a key reference; P4). bootstrap/exec carry
// strategy-specific messages naming why each cannot distribute.
var authMatrix = map[domain.CredentialStrategy]authCapability{
	domain.CredPool: {
		WorkerRejection: "api: an inline credential pool is not supported with distributed workers (only a reference-only source pool fans out; ship a credential source instead)",
	},
	domain.CredLogin: {
		WorkerRejection: "api: an inline credential pool is not supported with distributed workers (only a reference-only source pool fans out; ship a credential source instead)",
	},
	domain.CredBootstrapSignup: {
		WorkerRejection: "api: the \"bootstrap-signup\" strategy is not supported with distributed workers (a bootstrap pool provisions per-node accounts and has no shared reference to fan out; distributed bootstrap is a follow-up)",
	},
	domain.CredMint: {
		AllowsWorkers: true,
	},
	domain.CredExec: {
		WorkerRejection: "api: the \"exec\" strategy is not supported with distributed workers (it runs a local command per user; remote command execution is not fanned out)",
	},
}

// allowsWorkers reports whether a strategy may run with distributed workers on the
// strength of a non-secret reference the worker resolves locally.
func allowsWorkers(strategy domain.CredentialStrategy) bool {
	c, ok := authMatrix[strategy]
	return ok && c.AllowsWorkers
}

// workerRejectionFor returns the rejection message for a strategy that cannot run
// with distributed workers, falling back to the generic inline-pool message for an
// unknown strategy (domain.CredentialPool.Validate already rejects those, so this
// fallback is defensive only).
func workerRejectionFor(strategy domain.CredentialStrategy) string {
	if c, ok := authMatrix[strategy]; ok {
		return c.WorkerRejection
	}
	return "api: an inline credential pool is not supported with distributed workers (only a reference-only source pool fans out; ship a credential source instead)"
}
