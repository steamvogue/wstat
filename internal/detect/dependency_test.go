package detect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIncludeDependencyInvalidation(t *testing.T) {
	for _, server := range []string{"apache", "nginx"} {
		t.Run(server, func(t *testing.T) {
			d := t.TempDir()
			sites := filepath.Join(d, "sites")
			if err := os.Mkdir(sites, 0700); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(d, "main.conf")
			site := filepath.Join(sites, "a.conf")
			write := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if server == "apache" {
				write(root, "IncludeOptional sites/*.conf\n")
			} else {
				write(root, "http {\ninclude sites/*.conf;\n}\n")
			}
			write(site, "# initial\n")
			var scan *ScanResult
			if server == "apache" {
				scan = ScanApacheConfig(root, nil)
			} else {
				scan = ScanNginxConfig(root)
			}
			if len(scan.Files) != 2 || len(scan.IncludePatterns) != 1 {
				t.Fatal(scan)
			}
			e := &cacheEntry{Version: cacheVersion, SavedAt: time.Now(), Report: &Report{}, Files: scan.Files, Patterns: scan.IncludePatterns}
			var ok bool
			e.Dependencies, ok = dependencyFingerprint(e.Files, e.Patterns)
			if !ok || !validDependencies(e) {
				t.Fatal("initial dependencies invalid")
			}
			fi, err := os.Stat(site)
			if err != nil {
				t.Fatal(err)
			}
			write(site, "# changed\n")
			if err = os.Chtimes(site, fi.ModTime(), fi.ModTime()); err != nil {
				t.Fatal(err)
			}
			if validDependencies(e) {
				t.Fatal("included-only edit not detected")
			}
			e.Dependencies, _ = dependencyFingerprint(e.Files, e.Patterns)
			newSite := filepath.Join(sites, "b.conf")
			write(newSite, "# new\n")
			if validDependencies(e) {
				t.Fatal("new glob member not detected")
			}
			e.Dependencies, _ = dependencyFingerprint(e.Files, e.Patterns)
			if err = os.Remove(newSite); err != nil {
				t.Fatal(err)
			}
			if validDependencies(e) {
				t.Fatal("removed glob member not detected")
			}
			e.Dependencies, _ = dependencyFingerprint(e.Files, e.Patterns)
			if err = os.Remove(site); err != nil {
				t.Fatal(err)
			}
			if validDependencies(e) {
				t.Fatal("removed included file not detected")
			}
		})
	}
}

func TestCacheSymlinkExpiryAndSchema(t *testing.T) {
	d := t.TempDir()
	a, b, link := filepath.Join(d, "a"), filepath.Join(d, "b"), filepath.Join(d, "link")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("same"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	e := &cacheEntry{Version: cacheVersion, SavedAt: time.Now(), Report: &Report{}, Files: []string{link}}
	e.Dependencies, _ = dependencyFingerprint(e.Files, nil)
	if !validDependencies(e) {
		t.Fatal("fresh cache rejected")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if validDependencies(e) {
		t.Fatal("symlink target change ignored")
	}
	e.Dependencies, _ = dependencyFingerprint(e.Files, nil)
	e.SavedAt = time.Now().Add(-cacheTTL - time.Second)
	if validDependencies(e) {
		t.Fatal("expired cache accepted")
	}
	e.SavedAt = time.Now()
	e.Version = 1
	if validDependencies(e) {
		t.Fatal("old schema accepted")
	}
}

func TestDisabledCacheDoesNotReadOrWrite(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := CachePath()
	if _, hit := RunCachedWithOptions(true, false); hit {
		t.Fatal("disabled cache used")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled cache wrote file", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, hit := RunCachedWithOptions(false, false); hit {
		t.Fatal("disabled cache read")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "corrupt cache" {
		t.Fatal("disabled cache changed file", err)
	}
}
