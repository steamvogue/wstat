package logsrc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steamvogue/wstat/internal/filetail"
)

func TestPartialSeedHandoff(t *testing.T) {
	p := filepath.Join(t.TempDir(), "access_log")
	if err := os.WriteFile(p, []byte("one\npart"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := Start([]Source{{Path: p}}, nil, 10, nil)
	defer tr.Stop()
	select {
	case l := <-tr.Ch:
		if l.Text != "one" || !l.Seeded {
			t.Fatal(l)
		}
	case <-time.After(time.Second):
		t.Fatal("seed timeout")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("ial\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	select {
	case l := <-tr.Ch:
		if l.Text != "partial" || l.Seeded {
			t.Fatal(l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("partial completion timeout")
	}
}

func TestRetainedDescriptorHandoff(t *testing.T) {
	for _, replacement := range []string{"new\n", "new-longer\n"} {
		p := filepath.Join(t.TempDir(), "access_log")
		if err := os.WriteFile(p, []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		seed, offset, skip, err := seedFile(f, 10)
		if err != nil || len(seed) != 1 || seed[0] != "old" {
			t.Fatal(seed, err)
		}
		if err = os.Rename(p, p+".1"); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, []byte(replacement), 0600); err != nil {
			t.Fatal(err)
		}
		r := filetail.New(p, f, offset, skip)
		lines, _, err := r.Read(1024)
		_ = r.Close()
		if err != nil || len(lines) != 1 || lines[0] != strings.TrimSpace(replacement) {
			t.Fatal(lines, err)
		}
	}
}

func TestExtensionlessAndEmptyLive(t *testing.T) {
	d := t.TempDir()
	for _, name := range []string{"access_log", "custom", "custom.1", "custom.gz", "custom-20261009"} {
		if err := os.WriteFile(filepath.Join(d, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	srcs := Discover([]string{d + "/*"}, nil)
	if len(srcs) != 5 {
		t.Fatal(srcs)
	}
	for _, s := range srcs {
		want := strings.Contains(filepath.Base(s.Path), ".") || strings.Contains(s.Path, "-20261009")
		if s.Replay != want {
			t.Fatal(s)
		}
	}
}

func TestPinsOutsideDefaultDirectoriesOnInitialAndRescanSources(t *testing.T) {
	d := t.TempDir()
	first, next := filepath.Join(d, "first-access_log"), filepath.Join(d, "next-access_log")
	glob := filepath.Join(d, "*access_log")
	pins := map[string]string{glob: "glob-host", next: "exact-host"}
	if err := os.WriteFile(first, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := Start(Discover([]string{glob}, pins), []string{glob}, 1, pins, WithRescanEvery(10*time.Millisecond))
	defer tr.Stop()
	expect := func(text, host string) {
		t.Helper()
		select {
		case line := <-tr.Ch:
			if line.Text != text || line.Source.Vhost != host {
				t.Fatal(line)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("pinned source did not produce a line")
		}
	}
	expect("first", "glob-host")
	if err := os.WriteFile(next, []byte("next\n"), 0600); err != nil {
		t.Fatal(err)
	}
	expect("next", "exact-host")
}

func TestZeroSeedAndGzipBounds(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "access_log")
	if err := os.WriteFile(p, []byte("history\npartial"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	lines, off, skip, err := seedFile(f, 0)
	_ = f.Close()
	if err != nil || len(lines) != 0 || off != 15 || !skip {
		t.Fatal(lines, off, skip, err)
	}
	p = filepath.Join(d, "a.gz")
	if err = os.WriteFile(p, gzipBytes(t, strings.Repeat("\n", 1025)), 0600); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seedGzipChecked(f, 10, 1024)
	_ = f.Close()
	if err == nil {
		t.Fatal("blank decompressed bytes bypassed limit")
	}
	if err = os.WriteFile(p, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = seedFile(f, 10)
	_ = f.Close()
	if err == nil {
		t.Fatal("invalid gzip accepted")
	}
}

func TestStopDuringRescan(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "access_log")
	if err := os.WriteFile(p, []byte("x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		tr := Start(nil, []string{d + "/*"}, 10, nil, WithRescanEvery(time.Millisecond))
		done := make(chan struct{})
		go func() {
			for j := 0; j < 20; j++ {
				tr.rescan()
			}
			close(done)
		}()
		tr.Stop()
		<-done
		tr.Stop()
	}
}
