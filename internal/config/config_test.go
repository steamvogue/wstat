package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Chdir(t.TempDir())
	l := Load()
	if len(l.Config.Source.Paths) != 0 {
		t.Errorf("default paths = %v", l.Config.Source.Paths)
	}
	if l.Config.Source.SeedLines != 1000 {
		t.Errorf("default seed = %d", l.Config.Source.SeedLines)
	}
	if !l.Config.Detect.Enabled || !l.Config.Detect.Cache {
		t.Errorf("detection must default on: %+v", l.Config.Detect)
	}
}

func TestLoadUserConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Chdir(t.TempDir())
	write := func(content string) {
		p := filepath.Join(cfgDir, "wstat", "config.toml")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`
[source]
paths = ["/var/log/apache2/x-access.log*"]
seed_lines = 500

[source.vhost]
"/var/log/apache2/x-access.log*" = "custom.host"

[detect]
cache = false
`)
	l := Load()
	if len(l.Config.Source.Paths) != 1 || l.Config.Source.Paths[0] != "/var/log/apache2/x-access.log*" {
		t.Errorf("paths = %v", l.Config.Source.Paths)
	}
	if l.Config.Source.SeedLines != 500 {
		t.Errorf("seed = %d", l.Config.Source.SeedLines)
	}
	if l.Config.Source.Vhost["/var/log/apache2/x-access.log*"] != "custom.host" {
		t.Errorf("pins = %v", l.Config.Source.Vhost)
	}
	if l.Config.Detect.Cache {
		t.Error("detect.cache = false must win over the default")
	}
	if l.Origins.Paths != l.UserPath {
		t.Errorf("paths origin = %q", l.Origins.Paths)
	}
}

func TestProjectOverridesUser(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	projDir := t.TempDir()

	writeAt := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeAt(filepath.Join(cfgDir, "wstat", "config.toml"),
		"[source]\npaths = [\"/user/paths\"]\nseed_lines = 200\n")
	writeAt(filepath.Join(projDir, "wstat.toml"),
		"[source]\npaths = [\"/proj/paths\"]\n")

	t.Chdir(projDir)
	l := Load()
	if l.Config.Source.Paths[0] != "/proj/paths" {
		t.Errorf("project paths must win: %v", l.Config.Source.Paths)
	}
	// Field not set by the project keeps the user value.
	if l.Config.Source.SeedLines != 200 {
		t.Errorf("seed = %d, user value must survive", l.Config.Source.SeedLines)
	}
	if l.ProjectPath != "wstat.toml" {
		t.Errorf("project path = %q", l.ProjectPath)
	}
}

func TestDetectDisableWins(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Chdir(t.TempDir())
	p := filepath.Join(cfgDir, "wstat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("[detect]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := Load()
	if l.Config.Detect.Enabled {
		t.Fatal("explicit enabled=false must win over default true")
	}
}

func TestSaveRoundtrip(t *testing.T) {
	var c Config
	c.Source.SeedLines = 42
	c.Source.Paths = []string{"/a*", "/b"}
	c.Source.Vhost = map[string]string{"/a*": "x"}
	c.Detect.Enabled = true
	c.Detect.Cache = false

	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	// Load it back via loadFile.
	fc, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fc.cfg.Source.SeedLines != 42 || !fc.hasPaths || !fc.hasVhost || fc.cfg.Detect.Cache {
		t.Errorf("roundtrip = %+v (hasPaths=%v hasVhost=%v)", fc.cfg, fc.hasPaths, fc.hasVhost)
	}
}

func TestSaveTemplate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if existed, err := SaveTemplate(p); existed || err != nil {
		t.Fatalf("template write: existed=%v err=%v", existed, err)
	}
	if existed, err := SaveTemplate(p); !existed || err != nil {
		t.Fatalf("second call must not overwrite: existed=%v err=%v", existed, err)
	}
}

func TestDataPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdgdata")
	if got := DataPath("detect.json"); got != "/xdgdata/wstat/detect.json" {
		t.Errorf("DataPath = %q", got)
	}
}
