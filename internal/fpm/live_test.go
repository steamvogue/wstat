package fpm

import (
	"os"
	"testing"
	"time"
)

// TestQueryStatusLive runs against a real php-fpm with pm.status_path
// enabled when WSTAT_FPM_E2E_ADDR is set (e.g. a private instance started
// for e2e validation). Skipped otherwise.
func TestQueryStatusLive(t *testing.T) {
	addr := os.Getenv("WSTAT_FPM_E2E_ADDR")
	if addr == "" {
		t.Skip("no live php-fpm e2e instance")
	}
	st, procs, err := QueryStatus("tcp", addr, "/status")
	if err != nil {
		t.Fatalf("QueryStatus: %v", err)
	}
	if st.Pool != "e2e" {
		t.Errorf("pool = %q", st.Pool)
	}
	if st.ProcessManager != "dynamic" {
		t.Errorf("pm = %q", st.ProcessManager)
	}
	if st.TotalProcesses < 1 || st.ActiveProcesses+st.IdleProcesses != st.TotalProcesses {
		t.Errorf("processes: total=%d active=%d idle=%d", st.TotalProcesses, st.ActiveProcesses, st.IdleProcesses)
	}
	if st.ListenQueue < 0 || st.ListenQueueLen < 0 {
		t.Errorf("queue fields negative: %+v", st)
	}
	t.Logf("status ok: %+v (procs=%d)", st, len(procs))

	// Disabled status path must be detected as such, not as a generic error.
	_, _, err = QueryStatus("tcp", addr, "/definitely-not-a-status-path")
	if !IsStatusDisabled(err) {
		t.Errorf("expected status-disabled error, got %v", err)
	}
	_ = time.Second
}
