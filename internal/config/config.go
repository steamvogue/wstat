// Package config loads, merges and saves wstat configuration files.
//
// Precedence (highest first): CLI flags > project ./wstat.toml > user
// $XDG_CONFIG_HOME/wstat/config.toml > built-in defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the effective wstat configuration.
type Config struct {
	Source struct {
		// Paths are explicit files/globs to tail. When set, zero-config
		// detection is skipped for source selection.
		Paths     []string `toml:"paths"`
		SeedLines int      `toml:"seed_lines"`
		// Vhost pins: path or glob -> vhost. Overrides detection and
		// filename attribution.
		Vhost map[string]string `toml:"vhost"`
	} `toml:"source"`
	Detect struct {
		Enabled bool `toml:"enabled"` // zero-config detection when no paths set
		Cache   bool `toml:"cache"`   // cache detection results between runs
	} `toml:"detect"`
}

// Origins records which file provided each part of the config.
type Origins struct {
	Paths    string
	SeedLine string
	Vhost    string
	Detect   string
}

// Loaded is a merged config plus per-field origins.
type Loaded struct {
	Config  Config
	Origins Origins
	// UserPath and ProjectPath are the files that were consulted.
	UserPath    string
	ProjectPath string
}

// Default returns the built-in defaults.
func Default() Config {
	var c Config
	c.Source.SeedLines = 1000
	c.Detect.Enabled = true
	c.Detect.Cache = true
	return c
}

// UserConfigPath returns $XDG_CONFIG_HOME/wstat/config.toml.
func UserConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "wstat", "config.toml")
}

// ProjectConfigPath returns ./wstat.toml if present, else "".
func ProjectConfigPath() string {
	if _, err := os.Stat("wstat.toml"); err == nil {
		return "wstat.toml"
	}
	return ""
}

// DataPath returns a file path under $XDG_DATA_HOME/wstat (default
// ~/.local/share/wstat), used for the detection cache.
func DataPath(name string) string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return name
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "wstat", name)
}

// Load merges defaults, the user config and the project config.
func Load() *Loaded {
	l := &Loaded{
		Config:   Default(),
		UserPath: UserConfigPath(),
	}
	l.ProjectPath = ProjectConfigPath()

	if fc, err := loadFile(l.UserPath); err == nil {
		overlay(&l.Config, fc, &l.Origins, l.UserPath)
	}
	if l.ProjectPath != "" {
		if fc, err := loadFile(l.ProjectPath); err == nil {
			overlay(&l.Config, fc, &l.Origins, l.ProjectPath)
		}
	}
	return l
}

// fileConfig is a parsed config plus which keys were explicitly present
// (so that `detect.enabled = false` from a file wins over the default).
type fileConfig struct {
	cfg        Config
	hasEnabled bool
	hasCache   bool
	hasPaths   bool
	hasSeed    bool
	hasVhost   bool
}

func loadFile(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	fc := &fileConfig{}
	if err := toml.Unmarshal(data, &fc.cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if d, ok := raw["detect"]; ok {
		_, fc.hasEnabled = d["enabled"]
		_, fc.hasCache = d["cache"]
	}
	if s, ok := raw["source"]; ok {
		_, fc.hasPaths = s["paths"]
		_, fc.hasSeed = s["seed_lines"]
		_, fc.hasVhost = s["vhost"]
	}
	return fc, nil
}

// overlay applies explicitly-present fields from src onto dst, recording
// origins. Later overlays (project) win per field.
func overlay(dst *Config, src *fileConfig, o *Origins, origin string) {
	if src.hasPaths && len(src.cfg.Source.Paths) > 0 {
		dst.Source.Paths = src.cfg.Source.Paths
		o.Paths = origin
	}
	if src.hasSeed && src.cfg.Source.SeedLines > 0 {
		dst.Source.SeedLines = src.cfg.Source.SeedLines
		o.SeedLine = origin
	}
	if src.hasVhost && len(src.cfg.Source.Vhost) > 0 {
		if dst.Source.Vhost == nil {
			dst.Source.Vhost = map[string]string{}
		}
		for k, v := range src.cfg.Source.Vhost {
			dst.Source.Vhost[k] = v
		}
		o.Vhost = origin
	}
	if src.hasEnabled {
		dst.Detect.Enabled = src.cfg.Detect.Enabled
		o.Detect = origin
	}
	if src.hasCache {
		dst.Detect.Cache = src.cfg.Detect.Cache
		if o.Detect == "" {
			o.Detect = origin
		}
	}
}

// Save writes the config to path (creating parent directories).
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// SaveTemplate writes the default config to path if the file does not
// exist yet; returns whether the file already existed.
func SaveTemplate(path string) (existed bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	}
	if err := Save(path, Default()); err != nil {
		return false, err
	}
	return false, nil
}
