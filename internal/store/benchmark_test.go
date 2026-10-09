package store

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/parser"
)

// Saturated, deterministic state: no source scheduling or fixture dependencies.
func benchmarkStore() *Store {
	s := New()
	for i := 0; i < 54000; i++ {
		r := rec(fmt.Sprintf("host-%02d", i%46), fmt.Sprintf("client-%05d", i%11331), "GET", fmt.Sprintf("/path-%05d", i%20000), 200+i%4*100, int64(i%1000))
		r.Bot = i%7 == 0
		r.Static = i%5 == 0
		s.AddSeed(r)
	}
	return s
}

func BenchmarkSnapshot(b *testing.B) {
	s := benchmarkStore()
	b.Logf("hosts=%d urls=%d clients=%d requests=%d GOMAXPROCS=%d", len(s.hosts), len(s.urls), len(s.clients), s.tot.reqs, runtime.GOMAXPROCS(0))
	for _, mib := range []int64{48, 256} {
		b.Run(fmt.Sprintf("limit%dMiB", mib), func(b *testing.B) {
			old := debug.SetMemoryLimit(mib << 20)
			defer debug.SetMemoryLimit(old)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.Snapshot(Filters{}, [3]SortKey{}, 200)
			}
			b.StopTimer()
			runtime.ReadMemStats(&after)
			b.ReportMetric(float64(after.NumGC-before.NumGC)/float64(b.N), "GC/op")
		})
	}
}

// This forces the rate update every snapshot, matching a 500ms dashboard tick.
func BenchmarkSnapshotDecay(b *testing.B) {
	s := benchmarkStore()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.lastFlush = time.Now().Add(-500 * time.Millisecond)
		s.cache = nil // measure rebuilding, including rate maintenance
		s.Snapshot(Filters{}, [3]SortKey{}, 200)
	}
}

func BenchmarkSnapshotCorpus(b *testing.B) {
	glob := os.Getenv("WSTAT_BENCH_CORPUS")
	if glob == "" {
		b.Skip("set WSTAT_BENCH_CORPUS to an optional corpus glob")
	}
	sources := logsrc.Discover([]string{glob}, nil)
	if len(sources) == 0 {
		b.Fatal("no corpus sources matched")
	}
	s := New()
	for _, src := range sources {
		lines, err := logsrc.ReadSeed(src.Path, 1000)
		if err != nil {
			b.Log(err)
		}
		for _, line := range lines {
			if r, ok := parser.Parse(line, src.Vhost); ok {
				s.AddSeed(r)
			}
		}
	}
	b.Logf("sources=%d requests=%d hosts=%d urls=%d clients=%d", len(sources), s.tot.reqs, len(s.hosts), len(s.urls), len(s.clients))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.lastFlush = time.Now().Add(-500 * time.Millisecond)
		s.cache = nil // measure rebuilding, including rate maintenance
		s.Snapshot(Filters{}, [3]SortKey{}, 200)
	}
}

func TestRetentionSoak(t *testing.T) {
	if os.Getenv("WSTAT_DIAGNOSTICS") != "1" {
		t.Skip("opt-in bounded-state retention diagnostic")
	}
	s := New()
	for batch := 0; batch < 4; batch++ {
		for i := 0; i < 50000; i++ {
			k := batch*50000 + i
			r := rec(fmt.Sprintf("host-%d", k%3000), fmt.Sprint(k), "GET", fmt.Sprintf("/%d", k), 200, 100)
			s.Add(r)
		}
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		t.Logf("requests=%d hosts=%d urls=%d clients=%d associations=%d retained_heap=%d gc=%d", s.tot.reqs, len(s.hosts), len(s.urls), len(s.clients), len(s.pairs), m.HeapAlloc, m.NumGC)
		if len(s.hosts) > maxHosts || len(s.urls) > maxKeys || len(s.clients) > maxKeys || len(s.pairs) > maxAssociations {
			t.Fatal("detail budget exceeded")
		}
	}
	runtime.KeepAlive(s)
}
