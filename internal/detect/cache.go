package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/steamvogue/wstat/internal/config"
)

// CacheKey identifies the host state a detection report depends on. When
// any component changes, the cached report is stale.
type CacheKey struct {
	Hostname      string `json:"hostname"`
	DistroID      string `json:"distro_id"`
	ApacheVersion string `json:"apache_version"`
	ApacheConfig  string `json:"apache_config"`
	ApacheMTime   int64  `json:"apache_config_mtime_ns"`
	ApacheSize    int64  `json:"apache_config_size"`
	NginxConfig   string `json:"nginx_config"`
	NginxMTime    int64  `json:"nginx_config_mtime_ns"`
}

type cacheEntry struct {
	Key     CacheKey  `json:"key"`
	SavedAt time.Time `json:"saved_at"`
	Report  *Report   `json:"report"`
}

// CachePath returns the on-disk location of the detection cache.
func CachePath() string { return config.DataPath("detect.json") }

// CacheKeyFor computes the current host's cache key cheaply (one binary
// probe + two stats; no config scan or log probing).
func CacheKeyFor() CacheKey {
	var k CacheKey
	p := ProbePlatform()
	k.Hostname, k.DistroID = p.Hostname, p.DistroID
	a := ProbeApache()
	if a.Found {
		k.ApacheVersion = a.Version
		if fi, err := os.Stat(a.ConfigFile); err == nil && !fi.IsDir() {
			k.ApacheConfig = a.ConfigFile
			k.ApacheMTime = fi.ModTime().UnixNano()
			k.ApacheSize = fi.Size()
		}
	}
	for _, p := range nginxConfigPaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			k.NginxConfig = p
			k.NginxMTime = fi.ModTime().UnixNano()
			break
		}
	}
	return k
}

// RunCached returns the host detection report, using the on-disk cache
// when it is valid. force bypasses and refreshes the cache. The second
// return value reports whether a valid cache was used.
func RunCached(force bool) (*Report, bool) {
	cur := CacheKeyFor()
	path := CachePath()
	if !force {
		if entry, ok := loadCacheEntry(path); ok && entry.Key == cur && entry.Report != nil {
			return entry.Report, true
		}
	}
	rep := Run()
	if rep != nil {
		saveCacheEntry(path, &cacheEntry{Key: cur, SavedAt: time.Now(), Report: rep})
	}
	return rep, false
}

// InvalidateCache removes the stored detection report.
func InvalidateCache() {
	_ = os.Remove(CachePath())
}

// CacheStatus describes the current cache for diagnostics.
func CacheStatus() string {
	entry, ok := loadCacheEntry(CachePath())
	if !ok {
		return "no cache (fresh probe each run)"
	}
	if entry.Key == CacheKeyFor() {
		return "valid cache from " + entry.SavedAt.Format("2006-01-02 15:04") + " (used at startup)"
	}
	return "stale cache from " + entry.SavedAt.Format("2006-01-02 15:04") + " (will re-probe)"
}

func loadCacheEntry(path string) (*cacheEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	return &e, true
}

func saveCacheEntry(path string, e *cacheEntry) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
