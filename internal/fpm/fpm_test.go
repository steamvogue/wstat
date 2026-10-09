package fpm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePoolConfDebian(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "www.conf")
	content := `; comment
[www]
user = myweb
group = www-data
listen = /run/php/php8.4-fpm.sock
listen.owner = www-data
pm = dynamic
pm.max_children = 5
pm.start_servers = 2
pm.status_path = /fpm-status
access.log = /var/log/php-fpm.www-access.log
access.format = "%R - %u %t \"%m %r\" %s %d %M"
slowlog = /var/log/php-fpm.www-slow.log
php_admin_value[error_log] = /var/log/php-fpm.www.log
`
	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pools := parsePoolConf(conf)
	if len(pools) != 1 {
		t.Fatalf("pools = %d, want 1: %+v", len(pools), pools)
	}
	p := pools[0]
	if p.Name != "www" || p.Listen != "/run/php/php8.4-fpm.sock" || p.Network != "unix" {
		t.Errorf("basic parse = %+v", p)
	}
	if p.PMMode != "dynamic" || p.MaxChildren != 5 {
		t.Errorf("pm = %s/%d", p.PMMode, p.MaxChildren)
	}
	if p.StatusPath != "/fpm-status" || p.AccessLog == "" || !p.HasLatency() {
		t.Errorf("status/access = %+v", p)
	}
	if p.SlowLog == "" || p.User != "myweb" {
		t.Errorf("slowlog/user = %+v", p)
	}
}

func TestParsePoolConfTCPAndGlobal(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "a.conf")
	content := `[global]
pid = /run/x.pid

[api]
listen = 127.0.0.1:9001
pm = static
pm.max_children = 4
`
	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pools := parsePoolConf(conf)
	if len(pools) != 1 || pools[0].Name != "api" {
		t.Fatalf("pools = %+v", pools)
	}
	if pools[0].Network != "tcp" || pools[0].Addr != "127.0.0.1:9001" {
		t.Errorf("listen parse = %+v", pools[0])
	}
	if pools[0].StatusPath != "" || pools[0].HasLatency() {
		t.Error("no status path / no latency by default")
	}
}

// TestParseAccessRealLines uses lines produced by the real php-fpm 8.4
// private e2e instance (access.format "%R - %u %t \"%m %r\" %s %d %M").
func TestParseAccessRealLines(t *testing.T) {
	cases := []struct {
		line             string
		ip, method, path string
		status           int
		latUs            int64
		mem              int64
	}{
		{
			`127.0.0.1 -  09/Oct/2026:19:25:52 +0200 "GET /status" 200 0.002 2097152`,
			"127.0.0.1", "GET", "/status", 200, 2000, 2097152,
		},
		{
			`127.0.0.1 -  09/Oct/2026:19:25:52 +0200 "GET /definitely-not-a-status-path" 404 0.000 2097152`,
			"127.0.0.1", "GET", "/definitely-not-a-status-path", 404, 0, 2097152,
		},
		{
			`10.1.2.3 - alice 09/Oct/2026:19:26:05 +0200 "POST /api/x?a=b" 500 1.5 4194304`,
			"10.1.2.3", "POST", "/api/x", 500, 1500000, 4194304,
		},
	}
	for _, c := range cases {
		r, ok := ParseAccess(c.line, "e2e")
		if !ok {
			t.Fatalf("parse failed: %q", c.line)
		}
		if r.IP != c.ip || r.Method != c.method || r.Path != c.path {
			t.Errorf("line %q -> ip=%s method=%s path=%s", c.line, r.IP, r.Method, r.Path)
		}
		if r.Status != c.status || r.LatencyUs != c.latUs || r.Bytes != c.mem {
			t.Errorf("line %q -> status=%d lat=%d mem=%d (want %d/%d/%d)",
				c.line, r.Status, r.LatencyUs, r.Bytes, c.status, c.latUs, c.mem)
		}
		if r.Vhost != "e2e" {
			t.Errorf("attribution = %q", r.Vhost)
		}
		if r.Time.Year() != 2026 {
			t.Errorf("time = %v", r.Time)
		}
	}
}

func TestParseAccessRejectsGarbage(t *testing.T) {
	for _, line := range []string{
		"",
		"hello world",
		`1.2.3.4 - - [09/Oct/2026:19:26:05 +0200] "GET / HTTP/1.1" 200 5 "-" "-"`, // apache shape, not fpm
	} {
		if _, ok := ParseAccess(line, "t"); ok {
			t.Errorf("unexpected parse: %q", line)
		}
	}
}

func TestNormalizeDuration(t *testing.T) {
	cases := map[float64]int64{
		0.002:  2000,    // PHP >= 8 fractional seconds
		1.5:    1500000, // seconds
		2000:   2000,    // legacy microseconds
		150000: 150000,  // legacy microseconds
	}
	for in, want := range cases {
		if got := normalizeDurationToUs(in); got != want {
			t.Errorf("normalize(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestParseSlowlogHead(t *testing.T) {
	e, ok := ParseSlowlogHead(`[09-Oct-2026 19:30:12]  [pool www] pid 12345`)
	if !ok || e.Pool != "www" || e.Time.Day() != 9 {
		t.Fatalf("slowlog head = %+v ok=%v", e, ok)
	}
	if _, ok := ParseSlowlogHead(`    script_filename = /x.php`); ok {
		t.Error("continuation lines must not parse as entries")
	}
}

func TestReadProcStats(t *testing.T) {
	ps := ReadProcStats()
	// On a host with php-fpm running (the e2e instance), we should see it.
	if ps.Masters+ps.Workers == 0 {
		t.Skip("no php-fpm processes visible")
	}
	t.Logf("masters=%d workers=%d pools=%v rss=%dKB", ps.Masters, ps.Workers, ps.ByPool, ps.RSSKB)
}

func TestAnyAlert(t *testing.T) {
	views := []PoolView{
		{Pool: Pool{Name: "www"}, Status: &Status{ListenQueue: 3}},
	}
	if ok, msg := AnyAlert(views); !ok || msg != "fpm:www queue=3" {
		t.Errorf("AnyAlert = %v %q", ok, msg)
	}
	views[0].Status.ListenQueue = 0
	views[0].Status.MaxChildrenReached = 7
	if ok, msg := AnyAlert(views); !ok || msg != "fpm:www max-children" {
		t.Errorf("AnyAlert = %v %q", ok, msg)
	}
	views[0].Status = nil
	if ok, _ := AnyAlert(views); ok {
		t.Error("no alert without status")
	}
}
