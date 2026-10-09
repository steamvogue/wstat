package fpm

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Pool is one discovered php-fpm pool configuration.
type Pool struct {
	Name         string `json:"name"`
	ConfFile     string `json:"conf_file"`
	Listen       string `json:"listen"`  // socket path or tcp addr
	Network      string `json:"network"` // "unix" | "tcp"
	Addr         string `json:"addr"`
	PMMode       string `json:"pm_mode"` // static|dynamic|ondemand
	MaxChildren  int    `json:"max_children"`
	StatusPath   string `json:"status_path"` // "" when not configured
	AccessLog    string `json:"access_log"`
	AccessFormat string `json:"access_format"`
	SlowLog      string `json:"slowlog"`
	User         string `json:"user"`
}

// DiscoverPools parses pool configuration files from the common distro
// locations (WSTAT_FPM_POOL_GLOB overrides the globs — comma-separated).
// Missing directories are skipped silently.
func DiscoverPools() []Pool {
	globs := []string{
		"/etc/php/*/fpm/pool.d/*.conf",
		"/etc/php-fpm.d/*.conf",
		"/usr/local/php*/etc/php-fpm.d/*.conf",
	}
	if env := os.Getenv("WSTAT_FPM_POOL_GLOB"); env != "" {
		globs = strings.Split(env, ",")
	}
	seen := map[string]bool{}
	var pools []Pool
	for _, g := range globs {
		matches, _ := filepath.Glob(g)
		sort.Strings(matches)
		for _, path := range matches {
			if seen[path] {
				continue
			}
			seen[path] = true
			pools = append(pools, parsePoolConf(path)...)
		}
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	return pools
}

// parsePoolConf parses an INI-style php-fpm pool file. Section names are
// pool names; unknown keys are ignored.
func parsePoolConf(path string) []Pool {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var pools []Pool
	var cur *Pool
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if strings.EqualFold(name, "global") {
				cur = nil
				continue
			}
			pools = append(pools, Pool{Name: name, ConfFile: path})
			cur = &pools[len(pools)-1]
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "listen":
			cur.Listen = v
			if n, a, ok := parseListen(v); ok {
				cur.Network, cur.Addr = n, a
			}
		case "pm":
			cur.PMMode = v
		case "pm.max_children":
			cur.MaxChildren = parseIntSafe(v)
		case "pm.status_path":
			cur.StatusPath = v
		case "access.log":
			cur.AccessLog = v
		case "access.format":
			cur.AccessFormat = v
		case "slowlog":
			cur.SlowLog = v
		case "user":
			cur.User = v
		}
	}
	return pools
}

func parseIntSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// HasLatency reports whether the pool's access format logs request duration
// (%d) — the requirement for latency columns.
func (p Pool) HasLatency() bool {
	return strings.Contains(p.AccessFormat, "%d") && p.AccessLog != ""
}

// CanDial reports whether the listen socket is connectable for the current
// user, with a remediation hint when not.
func (p Pool) CanDial() (bool, string) {
	if p.Network == "" {
		return false, "no listen address in pool config"
	}
	conn, err := dialTimeout(p.Network, p.Addr, probeTimeout)
	if err == nil {
		_ = conn.Close()
		return true, ""
	}
	switch {
	case errors.Is(err, syscall.EACCES):
		return false, "socket not accessible by this user; fix: usermod -aG <socket-group> $USER (then re-login), or setfacl -m u:$USER:rw " + p.Addr
	case errors.Is(err, syscall.ENOENT):
		return false, "pool not running (socket missing)"
	default:
		return false, "dial failed: " + err.Error()
	}
}
