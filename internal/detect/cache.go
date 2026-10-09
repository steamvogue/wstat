package detect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	Key             CacheKey  `json:"key"`
	SavedAt         time.Time `json:"saved_at"`
	Report          *Report   `json:"report"`
	Version         int       `json:"version"`
	Files, Patterns []string
	Dependencies    string `json:"dependencies"`
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
const cacheVersion = 2
const cacheTTL = 5 * time.Minute

func RunCached(force bool) (*Report, bool) { return RunCachedWithOptions(force, true) }

// A disabled cache performs neither reads nor writes. Force only bypasses
// reads when caching is enabled; it never overrides the user's disable flag.
func RunCachedWithOptions(force, enabled bool) (*Report, bool) {
	if !enabled {
		return Run(), false
	}
	cur := CacheKeyFor()
	path := CachePath()
	if !force {
		if entry, ok := loadCacheEntry(path); ok && entry.Key == cur && validDependencies(entry) {
			return entry.Report, true
		}
	}
	rep := Run()
	if rep != nil {
		files, patterns := reportDependencies(rep)
		deps, ok := dependencyFingerprint(files, patterns)
		if ok {
			saveCacheEntry(path, &cacheEntry{Key: cur, SavedAt: time.Now(), Report: rep, Version: cacheVersion, Files: files, Patterns: patterns, Dependencies: deps})
		}
	}
	return rep, false
}
func reportDependencies(rep *Report) (files, patterns []string) {
	for _, scan := range []*ScanResult{rep.Scan, rep.NginxScan} {
		if scan != nil {
			files = append(files, scan.Files...)
			patterns = append(patterns, scan.IncludePatterns...)
		}
	}
	return uniquePaths(files), uniquePaths(patterns)
}
func uniquePaths(paths []string) []string {
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
func dependencyFingerprint(files, patterns []string) (string, bool) {
	if len(files) > 1024 || len(patterns) > 1024 {
		return "", false
	}
	h := sha256.New()
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", false
		}
		_, _ = fmt.Fprintf(h, "pattern:%q\n", pattern)
		for _, p := range matches {
			_, _ = fmt.Fprintf(h, "match:%q\n", p)
		}
	}
	for _, path := range files {
		_, _ = fmt.Fprintf(h, "file:%q\n", path)
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			_, _ = fmt.Fprintf(h, "missing:%v\n", err)
			continue
		}
		_, _ = fmt.Fprintf(h, "target:%q\n", target)
		f, err := os.Open(path)
		if err != nil {
			_, _ = fmt.Fprintf(h, "unreadable:%v\n", err)
			continue
		}
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			_ = f.Close()
			return "", false
		}
		_, _ = fmt.Fprintf(h, "metadata:%d:%d:%v\n", fi.ModTime().UnixNano(), fi.Size(), fi.Mode())
		n, err := io.Copy(h, io.LimitReader(f, 1<<20+1))
		_ = f.Close()
		if err != nil || n > 1<<20 {
			return "", false
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), true
}
func validDependencies(e *cacheEntry) bool {
	age := time.Since(e.SavedAt)
	if e.Version != cacheVersion || e.Report == nil || age < 0 || age > cacheTTL {
		return false
	}
	deps, ok := dependencyFingerprint(e.Files, e.Patterns)
	return ok && deps == e.Dependencies
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
	if entry.Key == CacheKeyFor() && validDependencies(entry) {
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
	f, err := os.CreateTemp(filepath.Dir(path), ".detect-*.tmp")
	if err != nil {
		return
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err = f.Chmod(0o644); err != nil {
		_ = f.Close()
		return
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return
	}
	if err = f.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
