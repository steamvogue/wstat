package fpm

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

var slowMu sync.Mutex

// countSlowLogLines counts timestamped entries in a slowlog (cheap,
// bounded scan from the end).
func countSlowLogLines(path string) int64 {
	slowMu.Lock()
	defer slowMu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	var n int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if _, ok := ParseSlowlogHead(sc.Text()); ok {
			n++
		}
	}
	return n
}

// DoctorText renders the php-fpm section for `wstat doctor`.
func DoctorText(pools []Pool) string {
	if len(pools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("php-fpm:\n")
	stats := ReadProcStats()
	for _, p := range pools {
		fmt.Fprintf(&b, "  pool %-12s %-28s", p.Name, listenLabel(p))
		if p.PMMode != "" {
			fmt.Fprintf(&b, " pm=%s/%d", p.PMMode, p.MaxChildren)
		}
		fmt.Fprintf(&b, "\n")
		if ok, remedy := p.CanDial(); !ok {
			fmt.Fprintf(&b, "    ! %s\n", remedy)
			continue
		}
		switch p.StatusPath {
		case "":
			b.WriteString("    - status page: not configured; add `pm.status_path = /fpm-status` to the pool to enable live stats\n")
		default:
			if _, _, err := QueryStatus(p.Network, p.Addr, p.StatusPath); err != nil {
				fmt.Fprintf(&b, "    - status page: %v\n", err)
			} else {
				b.WriteString("    - status page: reachable\n")
			}
		}
		if w := stats.ByPool[p.Name]; w > 0 {
			fmt.Fprintf(&b, "    - processes: %d worker(s) running\n", w)
		} else {
			b.WriteString("    - processes: none observed (pool idle or stopped)\n")
		}
		if p.HasLatency() {
			fmt.Fprintf(&b, "    - access log with duration: %s\n", p.AccessLog)
		}
	}
	return b.String()
}

func listenLabel(p Pool) string {
	if p.Listen == "" {
		return "(no listen)"
	}
	return p.Listen
}
