package runspec

import "github.com/chordpli/tmula/server/internal/domain"

// WorkerRejectionForTest exposes workerRejectionFor to the external test package
// so the single-source-of-truth assertion can read the authmatrix table without
// widening the package's production surface. (Go build convention: symbols in
// *_test.go files are compiled only for tests.)
func WorkerRejectionForTest(strategy domain.CredentialStrategy) string {
	return workerRejectionFor(strategy)
}
