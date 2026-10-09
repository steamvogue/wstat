package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidConfigurationDiagnostics(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	for _, text := range []string{"[source\n", "[source]\nseed_lines = -1\n", "[source]\nseed_lines = 100001\n", "[source]\npaths = [\"[broken\"]\n", "[detect]\ncache = \"false\"\n", "[fpm]\nenabeld = false\n"} {
		if err := os.WriteFile("wstat.toml", []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		l := Load()
		if l.Err() == nil || len(l.Diagnostics) != 1 || l.Diagnostics[0].Path != "wstat.toml" {
			t.Fatal("invalid config accepted", text, l)
		}
		if err := ValidateFile("wstat.toml"); err == nil {
			t.Fatal("invalid edit accepted")
		}
	}
	if err := os.Remove("wstat.toml"); err != nil {
		t.Fatal(err)
	}
	if l := LoadWithPath("missing.toml"); l.Err() == nil {
		t.Fatal("explicit missing file ignored")
	}
	if l := Load(); l.Err() != nil {
		t.Fatal("missing optional files rejected", l.Err())
	}
}

func TestExplicitZeroFalseAndEmptyPrecedence(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	t.Chdir(t.TempDir())
	if err := Save(UserConfigPath(), Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("wstat.toml", []byte("[source]\nseed_lines = 0\npaths = []\n[detect]\ncache = false\n[fpm]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l := Load()
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if l.Config.Source.SeedLines != 0 || l.Config.Detect.Cache || l.Config.FPM.Enabled || len(l.Config.Source.Paths) != 0 {
		t.Fatal(l.Config)
	}
	p := filepath.Join(t.TempDir(), "explicit.toml")
	if err := os.WriteFile(p, []byte("[source]\nseed_lines = 5\n[fpm]\nenabled = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l = LoadWithPath(p)
	if l.Err() != nil || l.Config.Source.SeedLines != 5 || !l.Config.FPM.Enabled || l.Config.Detect.Cache {
		t.Fatal(l)
	}
}

func TestUnreadableOrDirectoryConfiguration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if err := os.Mkdir("wstat.toml", 0700); err != nil {
		t.Fatal(err)
	}
	if l := Load(); l.Err() == nil {
		t.Fatal("directory treated as missing config")
	}
}
