package detect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRoundtripAndValidity(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := CachePath()

	// No cache initially.
	if _, ok := loadCacheEntry(path); ok {
		t.Fatal("unexpected existing cache")
	}

	// Save an entry with a fabricated key: must be invalid on this host.
	key := CacheKeyFor()
	key.ApacheMTime += 12345 // simulate a config change
	rep := Run()
	saveCacheEntry(path, &cacheEntry{Key: key, SavedAt: time.Now(), Report: rep})

	if got, ok := loadCacheEntry(path); !ok || got.Key != key {
		t.Fatalf("roundtrip failed: %+v ok=%v", got, ok)
	}
	// The tampered key no longer matches the current host: cache invalid.
	if e, ok := loadCacheEntry(path); ok && e.Key == CacheKeyFor() {
		t.Fatal("tampered key must not validate")
	}
	// Status reports staleness.
	if s := CacheStatus(); s == "" || s[:5] != "stale" && s[:2] != "no" {
		t.Errorf("CacheStatus = %q, want stale", s)
	}
}

func TestRunCachedForceAndHit(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	// Force run: fresh probe, writes cache.
	_, fromCache := RunCached(true)
	if fromCache {
		t.Fatal("forced run must not read the cache")
	}
	if _, ok := loadCacheEntry(CachePath()); !ok {
		t.Fatal("forced run must write the cache")
	}
	// Second run with the same host state: cache hit.
	rep, fromCache := RunCached(false)
	if !fromCache {
		t.Fatal("expected cache hit on second run")
	}
	if rep == nil || rep.Generated.IsZero() {
		t.Fatal("cached report missing")
	}
	// Invalidate: next run is fresh again.
	InvalidateCache()
	if _, fromCache := RunCached(false); fromCache {
		t.Fatal("cache must be gone after InvalidateCache")
	}
}

func TestCacheKeySensitiveToConfigMTime(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "apache2.conf")
	if err := os.WriteFile(conf, []byte("ServerName t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi1, _ := os.Stat(conf)
	k1 := CacheKey{ApacheConfig: conf, ApacheMTime: fi1.ModTime().UnixNano(), ApacheSize: fi1.Size()}
	// Rewrite with different size after touching mtime.
	time.Sleep(2 * time.Millisecond)
	if err := os.WriteFile(conf, []byte("ServerName t changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Stat(conf)
	k2 := CacheKey{ApacheConfig: conf, ApacheMTime: fi2.ModTime().UnixNano(), ApacheSize: fi2.Size()}
	if k1 == k2 {
		t.Fatal("key must change when the config file changes")
	}
}
