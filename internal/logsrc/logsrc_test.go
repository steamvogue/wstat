package logsrc

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVhostFromFilename(t *testing.T) {
	cases := map[string]string{
		"cms.local-access.log":       "cms.local",
		"example.com.access.log":     "example.com",
		"nodes-ssl-access.log":       "nodes", // 443 variant merges into the domain vhost
		"access.log":                 "default",
		"a-access.log":               "a",
		"my-site.com-access.log":     "my-site.com",
		"other_vhosts_access.log":    "other_vhosts",
		"prod.example.io-access.log": "prod.example.io",
		// cPanel-style remote host naming (samples/): 80 and 443 merge
		"allapotensmedel.com-ssl-access.log": "allapotensmedel.com",
		"ayudadiabetes.com-access.log":       "ayudadiabetes.com",
		"ayudadiabetes.com-ssl-access.log":   "ayudadiabetes.com",
		"pharmaplax.com-ssl-access.log":      "pharmaplax.com",
	}
	for name, want := range cases {
		if got := vhostFromFilename(name); got != want {
			t.Errorf("vhostFromFilename(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestVhostFromRotatedNames(t *testing.T) {
	cases := map[string]string{
		"cms.local-access.log.12.gz":  "cms.local",
		"access.log.1":                "default",
		"access.log.2.gz":             "default",
		"foo.com-ssl-access.log.3.gz": "foo.com",
	}
	for name, want := range cases {
		got := vhostFromFilename(rotationSuffix.ReplaceAllString(name, ""))
		if got != want {
			t.Errorf("rotated %q -> vhost %q, want %q", name, got, want)
		}
	}
}

func TestDiscoverClassifiesReplay(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-access.log", "1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] \"GET / HTTP/1.1\" 200 1 \"-\" \"-\"\n")
	write("a-access.log.1", "x\n")
	if err := os.WriteFile(filepath.Join(dir, "b-access.log.2.gz"), gzipBytes(t, "y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs := Discover([]string{filepath.Join(dir, "a-access.log*"), filepath.Join(dir, "b-access.log*")})
	byPath := map[string]Source{}
	for _, s := range srcs {
		byPath[filepath.Base(s.Path)] = s
	}
	if len(srcs) != 3 {
		t.Fatalf("discover = %d sources, want 3: %+v", len(srcs), srcs)
	}
	if byPath["a-access.log"].Replay {
		t.Error("plain .log must be live")
	}
	if !byPath["a-access.log.1"].Replay || !byPath["b-access.log.2.gz"].Replay {
		t.Error("rotated files must be replay-only")
	}
	if byPath["a-access.log"].Vhost != "a" || byPath["b-access.log.2.gz"].Vhost != "b" {
		t.Errorf("vhosts = %+v", byPath)
	}
}

func gzipBytes(t *testing.T, content string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGzSeed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "site-access.log.5.gz")
	var content strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&content, "1.2.3.4 - - [21/Oct/2025:14:26:%02d +0200] \"GET /p%d HTTP/1.1\" 200 1 \"-\" \"-\"\n", i%60, i)
	}
	if err := os.WriteFile(p, gzipBytes(t, content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, _ := seedLines(p, 10)
	if len(lines) != 10 {
		t.Fatalf("gz seed = %d lines, want 10", len(lines))
	}
	if !strings.Contains(lines[9], "/p30") {
		t.Errorf("gz seed newest = %q", lines[9])
	}
	if !strings.Contains(lines[0], "/p21") {
		t.Errorf("gz seed oldest = %q, want /p21", lines[0])
	}
}

func TestReplayOnlyDoesNotTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "site-access.log.1")
	if err := os.WriteFile(p, []byte("old1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := Start([]string{p}, 10)
	defer tr.Stop()
	deadline := time.After(2 * time.Second)
	select {
	case l := <-tr.Ch:
		if l.Text != "old1" || !l.Seeded {
			t.Fatalf("unexpected first line %+v", l)
		}
	case <-deadline:
		t.Fatal("replay source never delivered history")
	}
	// Append: replay-only sources must not deliver it.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("new1\n")
	f.Close()
	quiet := time.After(700 * time.Millisecond)
	select {
	case l := <-tr.Ch:
		t.Fatalf("replay source tailed: %+v", l)
	case <-quiet:
	}
}

func TestDiscoverSkipsErrorLogs(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a-access.log", "a-error.log", "b-access.log"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := Discover([]string{filepath.Join(dir, "*access*.log"), filepath.Join(dir, "*error*.log")})
	if len(got) != 2 {
		t.Fatalf("discover = %+v, want 2 sources", got)
	}
	for _, s := range got {
		if strings.Contains(s.Path, "error") {
			t.Errorf("error log leaked: %+v", s)
		}
	}
}

func TestSeedLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "seed.log")
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("line\n")
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got, offset := seedLines(p, 10)
	if len(got) != 10 {
		t.Fatalf("seedLines = %d lines, want 10", len(got))
	}
	if got[0] != "line" || got[9] != "line" {
		t.Fatalf("bad seed content: %q", got[0])
	}
	if offset != int64(len(sb.String())) {
		t.Fatalf("offset = %d, want %d", offset, len(sb.String()))
	}
}

// TestTailRotation covers the DoD rotation requirement: appended lines are
// never lost or double-counted across rename+recreate (logrotate default),
// in-place truncate, and startup seeding.
func TestTailRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "site-access.log")
	write := func(s string) {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	write("first\n")

	srcs := Discover([]string{p})
	if len(srcs) != 1 || srcs[0].Vhost != "site" {
		t.Fatalf("discover = %+v", srcs)
	}
	tr := Start([]string{p}, 10)

	counts := map[string]int{}
	deadline := time.After(8 * time.Second)
	collect := func(wantDistinct int) {
		for len(counts) < wantDistinct {
			select {
			case l := <-tr.Ch:
				counts[l.Text]++
			case <-deadline:
				t.Fatalf("timeout: got %d distinct lines (%v)", len(counts), counts)
			}
		}
	}
	collect(1) // "first" comes from the seed

	// Append two more lines (tail must pick these up, not re-deliver seed).
	write("second\nthird\n")
	collect(3)

	// logrotate default: rename + create fresh file at same path.
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	write("fourth\n")
	collect(4)

	// Truncate in place (copytruncate).
	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	write("fifth\n")
	collect(5)

	tr.Stop()
	for k, n := range counts {
		if n != 1 {
			t.Errorf("line %q delivered %d times (double count)", k, n)
		}
		switch k {
		case "first", "second", "third", "fourth", "fifth":
		default:
			t.Errorf("unexpected line %q", k)
		}
	}
}

func TestSeedStartsPopulated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pop-access.log")
	if err := os.WriteFile(p, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs := Discover([]string{p})
	if len(srcs) != 1 || srcs[0].Vhost != "pop" {
		t.Fatalf("discover = %+v", srcs)
	}
	tr := Start([]string{p}, 10)
	defer tr.Stop()
	counts := map[string]int{}
	deadline := time.After(2 * time.Second)
	for len(counts) < 3 {
		select {
		case l := <-tr.Ch:
			counts[l.Text]++
		case <-deadline:
			t.Fatalf("only %d seeded lines delivered", len(counts))
		}
	}
	// No further writes: any extra delivery within a grace period is a dupe.
	quiet := time.After(300 * time.Millisecond)
	for {
		select {
		case l := <-tr.Ch:
			t.Errorf("duplicate seeded line %q", l.Text)
		case <-quiet:
			return
		}
	}
}
