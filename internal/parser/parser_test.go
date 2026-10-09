package parser

import (
	"os"
	"strings"
	"testing"
)

func TestParseCombined(t *testing.T) {
	line := `192.168.100.219 - - [21/Oct/2025:14:26:43 +0200] "GET /panel/login HTTP/1.1" 200 48571 "http://cms.local/panel/site" "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/140.0.0.0"`
	r, ok := Parse(line, "fallback.test")
	if !ok {
		t.Fatal("expected parse ok")
	}
	if r.IP != "192.168.100.219" {
		t.Errorf("IP = %q", r.IP)
	}
	if r.Vhost != "fallback.test" {
		t.Errorf("Vhost = %q, want fallback", r.Vhost)
	}
	if r.Method != "GET" || r.Path != "/panel/login" {
		t.Errorf("req = %s %s", r.Method, r.Path)
	}
	if r.Status != 200 || r.Bytes != 48571 {
		t.Errorf("status/bytes = %d/%d", r.Status, r.Bytes)
	}
	if r.Time.Year() != 2025 || r.Time.Month() != 10 || r.Time.Day() != 21 || r.Time.Hour() != 14 {
		t.Errorf("time = %v", r.Time)
	}
	_, off := r.Time.Zone()
	if off != 2*3600 {
		t.Errorf("offset = %d", off)
	}
}

func TestParseVhostCombined(t *testing.T) {
	line := `cms.local:80 192.168.100.219 - - [21/Oct/2025:14:26:43 +0200] "POST /api HTTP/1.1" 404 128 "-" "curl/8.5.0"`
	r, ok := Parse(line, "fallback.test")
	if !ok {
		t.Fatal("expected parse ok")
	}
	if r.Vhost != "cms.local" {
		t.Errorf("Vhost = %q", r.Vhost)
	}
	if r.Status != 404 || r.Bytes != 128 {
		t.Errorf("status/bytes = %d/%d", r.Status, r.Bytes)
	}
	if !r.Bot {
		t.Error("curl should be marked bot")
	}
	// IP-literal vhost prefix (default vhost with ServerName set to an IP).
	r2, ok2 := Parse(`127.0.0.1:80 10.0.0.9 - - [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" 200 5 "-" "-"`, "fb")
	if !ok2 || r2.Vhost != "127.0.0.1" || r2.IP != "10.0.0.9" {
		t.Errorf("ip-vhost parse = %+v ok=%v", r2, ok2)
	}
	// IPv6 client must not be mistaken for a vhost prefix.
	r3, ok3 := Parse(`2001:db8::1 - - [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" 200 5 "-" "-"`, "fb")
	if !ok3 || r3.IP != "2001:db8::1" || r3.Vhost != "fb" {
		t.Errorf("ipv6 parse = %+v ok=%v", r3, ok3)
	}
}

func TestParseEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		line string
		ok   bool
		ip   string
	}{
		{"empty", "", false, ""},
		{"garbage", "hello world", false, ""},
		{"ipv6", `2001:db8::1 - - [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" 200 10 "-" "-"`, true, "2001:db8::1"},
		{"dash bytes", `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET /a HTTP/1.1" 304 - "-" "-"`, true, "1.2.3.4"},
		{"bad request line", `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "-" 400 0 "-" "-"`, true, "1.2.3.4"},
		{"query string", `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET /x?foo=bar&baz=1 HTTP/1.1" 200 5 "-" "-"`, true, "1.2.3.4"},
		{"no ua", `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" 200 5`, true, "1.2.3.4"},
		{"bad status", `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" xx 5 "-" "-"`, false, ""},
		{"no tz offset (tolerated as utc)", `1.2.3.4 - - [21/Oct/2025:14:26:43] "GET / HTTP/1.1" 200 5 "-" "-"`, true, "1.2.3.4"},
		{"crlf", `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" 200 5 "-" "-"` + "\r", true, "1.2.3.4"},
		{"escaped quotes in ua", `1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" 200 5 "-" "foo\"bar"`, true, "1.2.3.4"},
		{"auth user", `1.2.3.4 - alice [21/Oct/2025:14:26:43 +0200] "GET / HTTP/1.1" 200 5 "-" "-"`, true, "1.2.3.4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := Parse(c.line, "test")
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (rec %+v)", ok, c.ok, r)
			}
			if ok && r.IP != c.ip {
				t.Errorf("ip = %q, want %q", r.IP, c.ip)
			}
		})
	}
}

func TestParseQueryStripped(t *testing.T) {
	r, ok := Parse(`1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET /x?a=b HTTP/1.1" 200 5 "-" "-"`, "t")
	if !ok || r.Path != "/x" {
		t.Fatalf("path = %q", r.Path)
	}
}

func TestFingerprintRealLog(t *testing.T) {
	data, err := os.ReadFile("testdata/combined.log")
	if err != nil {
		t.Skip("no fixture")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got := Fingerprint(lines); got < 0.9 {
		t.Errorf("fingerprint on real cms.local log = %.2f, want >= 0.9", got)
	}
}

func BenchmarkParse(b *testing.B) {
	data, err := os.ReadFile("testdata/combined.log")
	if err != nil {
		b.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		b.Fatal("empty fixture")
	}
	// Grow the corpus so per-iteration overhead of the loop is negligible.
	var bench []string
	for len(bench) < 100000 {
		bench = append(bench, lines...)
	}
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for i := 0; i < b.N; i++ {
		if _, ok := Parse(bench[n%len(bench)], "bench.local"); !ok {
			b.Fatal("line failed to parse")
		}
		n++
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "lines/s")
}
