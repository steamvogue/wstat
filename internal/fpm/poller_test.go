package fpm

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSlowlogIncrementalRotationAndSharedPools(t *testing.T) {
	p := filepath.Join(t.TempDir(), "slow.log")
	entry := func(pool string) string { return "[09-Oct-2026 19:30:12] [pool " + pool + "] pid 1\n" }
	if err := os.WriteFile(p, []byte(entry("a")), 0600); err != nil {
		t.Fatal(err)
	}
	c := newSlowCounter(p)
	defer func() { _ = c.reader.Close() }()
	c.poll()
	if len(c.counts) != 0 {
		t.Fatal("history counted")
	}
	appendText := func(text string) {
		t.Helper()
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(text)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	appendText(entry("a") + entry("b") + "[09-Oct-2026 19:30:12] [pool a]")
	c.poll()
	c.poll()
	if c.counts["a"] != 1 || c.counts["b"] != 1 {
		t.Fatal(c.counts)
	}
	appendText(" pid 2\n")
	c.poll()
	if c.counts["a"] != 2 {
		t.Fatal(c.counts)
	}
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(entry("b")), 0600); err != nil {
		t.Fatal(err)
	}
	c.poll()
	if c.counts["a"] != 2 || c.counts["b"] != 2 {
		t.Fatal(c.counts)
	}
	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	c.poll()
	appendText(entry("b"))
	c.poll()
	if c.counts["b"] != 3 {
		t.Fatal(c.counts)
	}
}

func TestPollerStallDoesNotBlockViewsOrCancellation(t *testing.T) {
	ln, err := net.Listen("unix", t.TempDir()+"/stall.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer func() { _ = c.Close() }()
		close(accepted)
		_, _ = io.Copy(io.Discard, c)
	}()
	p := NewPoller([]Pool{{Name: "stalled", Network: "unix", Addr: ln.Addr().String(), StatusPath: "/status"}}, time.Hour)
	defer p.Stop()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not start")
	}
	read := make(chan []PoolView, 1)
	go func() { read <- p.Views() }()
	select {
	case views := <-read:
		if len(views) != 1 {
			t.Fatal(views)
		}
	case <-time.After(time.Second):
		t.Fatal("Views blocked by probe")
	}
	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel active probe")
	}
	<-serverDone
}

func BenchmarkSlowlogIdle(b *testing.B) {
	p := filepath.Join(b.TempDir(), "slow.log")
	f, err := os.Create(p)
	if err != nil {
		b.Fatal(err)
	}
	if err = f.Truncate(64 << 20); err != nil {
		b.Fatal(err)
	}
	_ = f.Close()
	c := newSlowCounter(p)
	defer func() { _ = c.reader.Close() }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.poll()
	}
}
