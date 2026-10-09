package fpm

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ProcStats are tier-2 process statistics gathered from /proc (always
// available to unprivileged users).
type ProcStats struct {
	Masters  int            `json:"masters"`
	Workers  int            `json:"workers"`
	RSSKB    int64          `json:"rss_kb"`
	CPUTicks int64          `json:"cpu_ticks"`
	ByPool   map[string]int `json:"by_pool"` // workers per pool from proctitles
}

// ReadProcStats scans /proc for php-fpm master and worker processes.
// Workers are grouped by pool name from their proctitle ("php-fpm: pool X").
func ReadProcStats() *ProcStats {
	ps := &ProcStats{ByPool: map[string]int{}}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ps
	}
	const pageSize = 4096 // arm64/x86_64 default; close enough for display
	for _, e := range entries {
		if !e.IsDir() || !isDigits(e.Name()) {
			continue
		}
		cmdline := readProcCmdline("/proc/" + e.Name() + "/cmdline")
		if !strings.Contains(cmdline, "php-fpm") {
			continue
		}
		switch {
		case strings.Contains(cmdline, "master process"):
			ps.Masters++
		case strings.Contains(cmdline, "pool "):
			ps.Workers++
			if name := poolNameFromTitle(cmdline); name != "" {
				ps.ByPool[name]++
			}
		}
		if rss, cpu, ok := readProcStat(e.Name()); ok {
			ps.RSSKB += rss / pageSize
			ps.CPUTicks += cpu
		}
	}
	return ps
}

func poolNameFromTitle(title string) string {
	i := strings.LastIndex(title, "pool ")
	if i < 0 {
		return ""
	}
	name := strings.TrimSpace(title[i+len("pool "):])
	if j := strings.IndexAny(name, " \t"); j > 0 {
		name = name[:j]
	}
	return name
}

func readProcCmdline(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(data), "\x00", " ")
}

// readProcStat extracts RSS (bytes) and CPU ticks from /proc/<pid>/stat.
func readProcStat(pid string) (rss int64, cpu int64, ok bool) {
	data, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return 0, 0, false
	}
	s := string(data)
	// Skip the comm field, which may contain spaces inside parentheses.
	if i := strings.LastIndexByte(s, ')'); i > 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	f := strings.Fields(s)
	// Fields after the state: (11)utime (12)stime ... (22)rss
	if len(f) < 21 {
		return 0, 0, false
	}
	utime, _ := strconv.ParseInt(f[11], 10, 64)
	stime, _ := strconv.ParseInt(f[12], 10, 64)
	rssPages, _ := strconv.ParseInt(f[21], 10, 64)
	return rssPages * int64(os.Getpagesize()), utime + stime, true
}

// SocketBacklog returns the current listen backlog of a unix socket via
// ss, best-effort (0 when unavailable).
func SocketBacklog(sockPath string) int {
	out, err := runSS("-xl")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && strings.HasSuffix(fields[len(fields)-1], sockPath) {
			// ss -xl: STATE Recv-Q Send-Q ... path (Recv-Q is the backlog)
			if len(fields) >= 2 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func runSS(args ...string) (string, error) {
	out, err := exec.Command("ss", args...).Output()
	return string(out), err
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ProcUptime returns the system uptime in seconds (unused pages trick kept
// minimal; /proc/uptime parse).
func ProcUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) < 1 {
		return 0
	}
	up, _ := strconv.ParseFloat(f[0], 64)
	return int64(up)
}
