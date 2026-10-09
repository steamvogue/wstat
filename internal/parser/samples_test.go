package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRemoteSamples validates the parser against real production logs from
// a remote per-domain host (samples/*access*.log, ~77 files / 240k+ lines).
// The corpus is local-only; the test skips when it is not present.
func TestRemoteSamples(t *testing.T) {
	files, _ := filepath.Glob("../../samples/*access*.log")
	if len(files) == 0 {
		t.Skip("no samples/ corpus")
	}
	total, good := 0, 0
	var badSamples []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) == "" {
				continue
			}
			total++
			if _, ok := Parse(l, "probe"); ok {
				good++
			} else if len(badSamples) < 3 {
				badSamples = append(badSamples, filepath.Base(f)+": "+l)
			}
		}
	}
	ratio := float64(good) / float64(total)
	if ratio < 0.999 {
		t.Errorf("parse ratio on real corpus = %.5f (%d bad of %d); samples:\n%s",
			ratio, total-good, total, strings.Join(badSamples, "\n"))
	}
	t.Logf("real corpus: %d/%d lines parse (%.5f%%) across %d files",
		good, total, ratio*100, len(files))
}

// TestRemoteSamplesThroughput measures end-to-end parse speed on the real
// corpus (excluding file IO).
func TestRemoteSamplesThroughput(t *testing.T) {
	files, _ := filepath.Glob("../../samples/*access*.log")
	if len(files) == 0 {
		t.Skip("no samples/ corpus")
	}
	var lines []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	start := time.Now()
	for i, l := range lines {
		if _, ok := Parse(l, "bench"); !ok {
			t.Fatalf("unparseable line %d: %q", i, l)
		}
	}
	elapsed := time.Since(start)
	t.Logf("parsed %d real lines in %v (%d lines/s)", len(lines), elapsed,
		int(float64(len(lines))/elapsed.Seconds()))
}

func TestDoubleSlashPathNormalized(t *testing.T) {
	r, ok := Parse(`1.2.3.4 - - [21/Oct/2025:14:26:43 +0200] "GET //sync.php?domain=x HTTP/1.1" 200 5 "-" "-"`, "t")
	if !ok || r.Path != "/sync.php" {
		t.Fatalf("path = %q ok=%v", r.Path, ok)
	}
}
