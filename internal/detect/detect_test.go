package detect

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(name string) string { return filepath.Join("testdata", name) }

func TestParseEnvvarsExpandsShellVars(t *testing.T) {
	vars := ParseEnvvars(fixture("debian/envvars"))
	if got := vars["APACHE_LOG_DIR"]; got != "/var/log/apache2" {
		t.Errorf("APACHE_LOG_DIR = %q, want /var/log/apache2 ($SUFFIX must expand to empty)", got)
	}
}

func TestScanApacheFixture(t *testing.T) {
	vars := ParseEnvvars(fixture("debian/envvars"))
	res := ScanApacheConfig(fixture("debian/main.conf"), vars)

	// Exact vhost attribution from <VirtualHost> ServerName.
	cases := map[string]string{
		"/var/log/apache2/otter.net-access.log":      "otter.net",
		"/var/log/apache2/falcon.com-access.log":     "falcon.com",
		"/var/log/apache2/badger.org-access.log":     "badger.org",
		"/var/log/apache2/gecko-ssl-access.log":      "admin.gecko.net",
		"/var/log/apache2/gecko-wildcard-access.log": "wildcard.gecko.net",
		"/var/log/apache2/koala.com-access.log":      "koala.net",
		"/var/log/apache2/lynx.net-access.log":       "lynx.net",
		"/var/log/apache2/wombat.com-access.log":     "wombat.com",
	}
	for path, want := range cases {
		if got := res.VhostByLog[path]; got != want {
			t.Errorf("VhostByLog[%s] = %q, want %q", path, got, want)
		}
	}
	// 000-default logs to access.log but has no ServerName: no mapping
	// (filename fallback applies at runtime).
	if _, ok := res.VhostByLog["/var/log/apache2/access.log"]; ok {
		t.Error("access.log must not be config-attributed (vhostless block)")
	}
	// Formats pinned per path.
	if res.FormatByLog["/var/log/apache2/otter.net-access.log"] != "combined" {
		t.Errorf("format pin = %v", res.FormatByLog["/var/log/apache2/otter.net-access.log"])
	}
	// Format table from LogFormat directives.
	if _, ok := res.LogFormats["vhost_combined"]; !ok {
		t.Error("vhost_combined format not recorded")
	}
	// Global catch-all log declared and expanded.
	found := false
	for _, l := range res.GlobalLogs {
		if l.Path == "/var/log/apache2/other_vhosts_access.log" && l.Format == "vhost_combined" {
			found = true
		}
	}
	if !found {
		t.Errorf("global other_vhosts log not found: %+v", res.GlobalLogs)
	}
	// 9 vhost blocks with logs (8 sites, gecko has two blocks).
	if len(res.Vhosts) != 9 {
		t.Errorf("vhosts = %d, want 9: %+v", len(res.Vhosts), res.Vhosts)
	}
	// IncludeOptional that matches nothing must not warn.
	for _, w := range res.Warnings {
		if strings.Contains(w, "missing-but-optional") {
			t.Errorf("IncludeOptional miss warned: %s", w)
		}
	}
}

func TestScanApacheIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "self.conf")
	if err := os.WriteFile(p, []byte("CustomLog ${APACHE_LOG_DIR}/x.log combined\nInclude self.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := ScanApacheConfig(p, map[string]string{"APACHE_LOG_DIR": dir})
	if len(res.Warnings) == 0 {
		t.Error("include cycle must produce a depth-limit warning")
	}
	if len(res.GlobalLogs) == 0 {
		t.Error("cycle must not prevent the first file from being scanned")
	}
}

func TestScanApachePipedLog(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "piped.conf")
	if err := os.WriteFile(p, []byte(`CustomLog "|/usr/bin/rotatelogs /var/log/x.%Y%m%d 86400" combined`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := ScanApacheConfig(p, nil)
	if len(res.GlobalLogs) != 1 || !res.GlobalLogs[0].Piped {
		t.Fatalf("piped log not flagged: %+v", res.GlobalLogs)
	}
	if len(res.VhostByLog) != 0 {
		t.Errorf("piped log must not be tailed: %+v", res.VhostByLog)
	}
}

func TestScanNginxFixture(t *testing.T) {
	res := ScanNginxConfig(fixture("nginx/nginx.conf"))
	if got := res.VhostByLog["/var/log/nginx/otter.net-access.log"]; got != "otter.net" {
		t.Errorf("nginx vhost = %q, want otter.net: %+v", got, res.VhostByLog)
	}
	if f := res.LogFormats["forge"]; !strings.Contains(f, "$remote_addr") {
		t.Errorf("forge format = %q", f)
	}
	if len(res.GlobalLogs) != 1 || res.GlobalLogs[0].Path != "/var/log/nginx/access.log" {
		t.Errorf("nginx global log = %+v", res.GlobalLogs)
	}
	if len(res.Vhosts) != 1 {
		t.Errorf("nginx vhosts = %d, want 1", len(res.Vhosts))
	}
}

func TestParseRotationFixture(t *testing.T) {
	data, err := os.ReadFile(fixture("debian/logrotate-apache2"))
	if err != nil {
		t.Fatal(err)
	}
	r := parseRotationConfig(data)
	if !r.Found || r.Schedule != "daily" || r.Keep != 14 || !r.Compress || r.CopyTruncate {
		t.Errorf("rotation = %+v, want daily/14/compress/no-copytruncate", r)
	}
}

func TestProbeLogFormats(t *testing.T) {
	dir := t.TempDir()
	const line = `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET /x HTTP/1.1" 200 5 "-" "test"`
	const vhostLine = `otter.net:80 ` + line
	const jsonLine = `{"ts":"2026-10-09","request":{"path":"/x"}}`
	const w3cLine = `#Fields: date time c-ip cs-method`

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if p := ProbeLog(write("a-access.log", strings.Repeat(line+"\n", 10))); p.Format != "combined" || p.Ratio != 1 {
		t.Errorf("combined probe = %+v", p)
	}
	if p := ProbeLog(write("b-access.log", strings.Repeat(vhostLine+"\n", 10))); p.Format != "vhost_combined" {
		t.Errorf("vhost_combined probe = %+v", p)
	}
	if p := ProbeLog(write("c.log", strings.Repeat(jsonLine+"\n", 5))); p.Format != "json" {
		t.Errorf("json probe = %+v", p)
	}
	if p := ProbeLog(write("e.log", w3cLine+"\n"+line+"\n")); p.Format != "w3c" && p.Format != "combined" {
		t.Errorf("w3c probe = %+v", p)
	}
	// gzip sampling
	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	if _, err := zw.Write([]byte(strings.Repeat(line+"\n", 10))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	gzPath := filepath.Join(dir, "f-access.log.2.gz")
	if err := os.WriteFile(gzPath, gzBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := ProbeLog(gzPath); p.Format != "combined" || p.Ratio != 1 {
		t.Errorf("gz probe = %+v", p)
	}
	// empty + missing
	if p := ProbeLog(write("g-access.log", "")); !p.Empty || !p.Readable {
		t.Errorf("empty probe = %+v", p)
	}
	if p := ProbeLog(filepath.Join(dir, "nope-access.log")); !p.Missing {
		t.Errorf("missing probe = %+v", p)
	}
}

func TestProbeLogUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permissions are bypassed")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "secret-access.log")
	if err := os.WriteFile(p, []byte("x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	probe := ProbeLog(p)
	if probe.Readable {
		t.Fatal("probe claims readable")
	}
	if !strings.Contains(probe.Reason, "fix:") {
		t.Errorf("remedy missing: %q", probe.Reason)
	}
}

func TestRunCompletes(t *testing.T) {
	rep := Run()
	if rep.Generated.IsZero() {
		t.Fatal("report not generated")
	}
	if rep.Platform.Hostname == "" {
		t.Error("hostname missing")
	}
	// Report must be JSON-serializable.
	if !strings.Contains(rep.JSON(), "\"platform\"") {
		t.Error("JSON output broken")
	}
	_ = rep.Doctor()
	_ = rep.Sources()
}
