package store

import (
	"runtime"
	"testing"
	"time"
)

func TestSnapshotCacheInvalidation(t *testing.T) {
	s := New()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.AddSeed(rec("a", "ip", "GET", "/", 200, 10))
	f := Filters{}
	sorts := [3]SortKey{}
	h, _, _, _, _, _ := s.Snapshot(f, sorts, 200)
	first := s.cache
	s.AddBad()
	_, _, _, _, _, bad := s.Snapshot(f, sorts, 200)
	if s.cache != first || bad != 1 {
		t.Fatal("bad counter lost or unnecessary rebuild")
	}
	now = now.Add(500 * time.Millisecond)
	s.Add(rec("a", "ip", "GET", "/", 500, 20))
	h2, _, _, _, _, _ := s.Snapshot(f, sorts, 200)
	if s.cache == first || h[0].Hits != 1 || h2[0].Hits != 2 || h2[0].Bytes != 30 {
		t.Fatal("ingestion invalidation/immutability")
	}
	first = s.cache
	now = now.Add(500 * time.Millisecond)
	h3, _, _, _, _, _ := s.Snapshot(f, sorts, 200)
	if s.cache == first || h3[0].Rate >= h2[0].Rate {
		t.Fatal("rate decay cache stayed stale")
	}
	f.Mask = MaskErr
	s.Snapshot(f, sorts, 200)
	if s.cache.key.mask != MaskErr {
		t.Fatal("mask invalidation")
	}
	f.Bots = 1
	s.Snapshot(f, sorts, 200)
	if s.cache.key.bots != 1 {
		t.Fatal("bot invalidation")
	}
	f.Static = -1
	f.Method = "POST"
	f.Hosts = map[string]bool{"b": true}
	f.Clients = map[string]bool{"other": true}
	f.Paths = map[string]bool{"/other": true}
	sorts[0] = SortBytes
	s.Snapshot(f, sorts, 1)
	if s.cache.key != cacheKey(f, sorts, 1) {
		t.Fatal("filter/sort/size invalidation")
	}
	f.Hosts["a"] = true
	s.Snapshot(f, sorts, 1)
	if s.cache.key.hosts != filterSetKey(f.Hosts) {
		t.Fatal("mutated set cached by pointer")
	}
}

func TestSnapshotAllocationBudget(t *testing.T) {
	s := benchmarkStore()
	s.Snapshot(Filters{}, [3]SortKey{}, 200)
	if n := testing.AllocsPerRun(100, func() { s.Snapshot(Filters{}, [3]SortKey{}, 200) }); n > 1 {
		t.Fatalf("idle snapshot allocations=%v", n)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 10; i++ {
		s.cache = nil
		s.Snapshot(Filters{}, [3]SortKey{}, 200)
	}
	runtime.ReadMemStats(&after)
	if bytes := (after.TotalAlloc - before.TotalAlloc) / 10; bytes > 256<<10 {
		t.Fatalf("rebuild allocated %d bytes; budget 256 KiB (baseline 23.4 MB)", bytes)
	}
	runtime.KeepAlive(s)
}
