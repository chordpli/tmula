package runspec

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain lets the runspec test binary re-exec itself as a tiny "token printer" helper,
// so the exec-strategy CredentialProvider test can drive a REAL child process (no shell,
// no external dependency) — fully portable across darwin/linux CI. When TMULA_EXEC_HELPER
// is set the process behaves as the helper the mode selects and exits; otherwise it runs
// the test suite normally. It mirrors the helper in auth/exec_test.go so the exec
// provider has a command to run in this package's tests too.
func TestMain(m *testing.M) {
	switch os.Getenv("TMULA_EXEC_HELPER") {
	case "":
		os.Exit(m.Run())
	case "bare":
		fmt.Printf("tok-%s-%s-%s\n", os.Getenv("TMULA_EXEC_IDX"), strings.Join(os.Args[1:], "|"), os.Getenv("TMULA_EXEC_EXTRA"))
		os.Exit(0)
	case "json":
		fmt.Printf(`{"access_token":"jtok-%s","refresh_token":"r-%s","expires_in":900,"username":"sub-%s"}`,
			os.Getenv("TMULA_EXEC_IDX"), os.Getenv("TMULA_EXEC_IDX"), os.Getenv("TMULA_EXEC_IDX"))
		os.Exit(0)
	case "empty":
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	case "hang":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode")
		os.Exit(2)
	}
}
