package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steamvogue/wstat/internal/fpm"
	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/store"
)

func TestMixedSourceRouting(t *testing.T) {
	p := fpm.NewPoller([]fpm.Pool{{Name: "www", AccessLog: "php.log"}}, 0)
	defer p.Stop()
	r := newIngestionRouter()
	r.poller = p
	ap, err := fpm.NewAccessParser(`%R - %u %t "%m %r" %s %{milliseconds}d %M`)
	if err != nil {
		t.Fatal(err)
	}
	r.services["php.log"] = ap
	s := store.New()
	r.ingest(s, logsrc.RawLine{Source: &logsrc.Source{Vhost: "web"}, Text: `127.0.0.1 - - [09/Oct/2026:19:25:52 +0200] "GET /index.php HTTP/1.1" 200 100 "-" "Mozilla/5.0"`})
	r.ingest(s, logsrc.RawLine{Source: &logsrc.Source{Path: "php.log", Vhost: "www", Kind: logsrc.FPM}, Text: `127.0.0.1 -  09/Oct/2026:19:25:52 +0200 "GET /index.php" 200 1 2097152`, Seeded: true})
	h, u, _, _, tot, bad := s.Snapshot(store.Filters{}, [3]store.SortKey{}, 200)
	v := p.Views()[0]
	if tot.Reqs != 1 || tot.Bytes != 100 || bad != 0 || len(h) != 1 || len(u) != 1 || u[0].LatencyUs != 0 {
		t.Fatal("service event entered web metrics", tot, u, bad)
	}
	if v.Access.Requests != 1 || v.Access.Seeded != 1 || v.Access.DurationUs != 1000 || v.Access.MemoryBytes != 2097152 {
		t.Fatal(v.Access)
	}
	r.ingest(s, logsrc.RawLine{Source: &logsrc.Source{Path: "php.log", Kind: logsrc.FPM}, Text: "invalid service line"})
	_, _, _, _, tot, bad = s.Snapshot(store.Filters{}, [3]store.SortKey{}, 200)
	if bad != 0 || tot.Reqs != 1 || p.Views()[0].Access.Bad != 1 {
		t.Fatal("bad PHP log mixed with web metrics")
	}
}

func TestSeedAndLiveIngestionConserveWebCounts(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "access_log")
	line := func(i int) string {
		return fmt.Sprintf("127.0.0.1 - - [09/Oct/2026:19:25:52 +0200] \"GET /%d HTTP/1.1\" 200 100 \"-\" \"Mozilla/5.0\"\n", i)
	}
	var seed strings.Builder
	for i := 0; i < 1000; i++ {
		seed.WriteString(line(i))
	}
	if err := os.WriteFile(path, []byte(seed.String()), 0600); err != nil {
		t.Fatal(err)
	}
	s := store.New()
	r := newIngestionRouter()
	tailer := logsrc.Start([]logsrc.Source{{Path: path, Vhost: "web"}}, []string{path}, 1000, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for raw := range tailer.Ch {
			r.ingest(s, raw)
		}
	}()
	defer func() { tailer.Stop(); <-done }()
	wait := func(want int64) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for {
			_, _, _, _, tot, bad := s.Snapshot(store.Filters{}, [3]store.SortKey{}, 10)
			if tot.Reqs == want && tot.Bytes == 100*want && bad == 0 {
				return
			}
			if time.Now().After(until) || tot.Reqs > want || bad != 0 {
				t.Fatalf("want %d complete records, got %+v bad=%d", want, tot, bad)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(1000)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var live strings.Builder
	for i := 1000; i < 1600; i++ {
		live.WriteString(line(i))
	}
	_, err = f.WriteString(live.String())
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	wait(1600)
}

// Subprocess tests execute the actual CLI dispatcher with isolated config/data.
func TestCLIHelper(t *testing.T) {
	if os.Getenv("WSTAT_CLI_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"wstat"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("wstat", flag.ExitOnError)
	os.Exit(run())
}
func cli(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "WSTAT_CLI_HELPER=1", "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}
func TestCLIConfigValidationAndZero(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "wstat.toml")
	if err := os.WriteFile(path, []byte("[source]\nseed_lines = 0\n[detect]\ncache = false\n[fpm]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := cli(t, d, "config", "show")
	if err != nil || !strings.Contains(out, "seed_lines = 0") || !strings.Contains(out, "disabled (no cache reads/writes)") {
		t.Fatal(out, err)
	}
	if err = os.WriteFile(path, []byte("[source]\nseed_lines = -1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = cli(t, d, "config", "show")
	if err == nil || !strings.Contains(out, "seed_lines") || strings.Contains(out, "effective configuration") {
		t.Fatal(out, err)
	}
	out, err = cli(t, d, "-config", "missing.toml")
	if err == nil || !strings.Contains(out, "missing.toml") {
		t.Fatal(out, err)
	}
}

func TestConfigEditRejectsInvalidSave(t *testing.T) {
	d := t.TempDir()
	editor := filepath.Join(d, "editor.sh")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf '[source]\\nseed_lines = -1\\n' > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	out, err := cli(t, d, "config", "edit")
	if err == nil || strings.Contains(out, "config saved:") || !strings.Contains(out, "seed_lines") {
		t.Fatal(out, err)
	}
}

func TestProfilesDisabledAndCreationErrors(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	stop, err := startProfiles("", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(d)
	if err != nil || len(entries) != 0 {
		t.Fatal("disabled profiling wrote files", entries, err)
	}
	if _, err = startProfiles(filepath.Join(d, "missing", "cpu"), ""); err == nil {
		t.Fatal("profile creation error ignored")
	}
	heap := filepath.Join(d, "heap.pprof")
	stop, err = startProfiles("", heap)
	if err != nil {
		t.Fatal(err)
	}
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(heap)
	if err != nil || fi.Size() == 0 {
		t.Fatal("empty heap profile", err)
	}
}

func TestProfileDelayAndPathValidation(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "cpu.pprof")
	if _, err := startProfiles(path, filepath.Join(d, ".", "cpu.pprof")); err == nil {
		t.Fatal("same output file accepted")
	}
	if _, err := startProfiles(path, "", -time.Second); err == nil {
		t.Fatal("negative delay accepted")
	}
	stop, err := startProfiles(path, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = stop(); err == nil || !strings.Contains(err.Error(), "before delayed start") {
		t.Fatal("early cancellation was not reported", err)
	}
	stop, err = startProfiles(path, "", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		t.Fatal("empty delayed CPU profile", err)
	}
}
