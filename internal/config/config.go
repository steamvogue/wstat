// Package config loads, merges and saves wstat configuration files.
//
// Precedence (highest first): CLI flags > project ./wstat.toml > user
// $XDG_CONFIG_HOME/wstat/config.toml > built-in defaults.
package config

import (
	"bytes"
	"errors"
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
	FPM struct {
		Enabled bool `toml:"enabled"`
	} `toml:"fpm"`
}

// Origins records which file provided each part of the config.
type Origins struct {
	Paths    string
	SeedLine string
	Vhost    string
	Detect   string
	FPM      string
}

// Loaded is a merged config plus per-field origins.
type Loaded struct {
	Config  Config
	Origins Origins
	// UserPath and ProjectPath are the files that were consulted.
	UserPath    string
	ProjectPath string
	Diagnostics []Diagnostic
}

// Diagnostic preserves the failing path and underlying read/parse error.
type Diagnostic struct {
	Path  string
	Cause error
}

func (d Diagnostic) Error() string { return d.Path + ": " + d.Cause.Error() }
func (d Diagnostic) Unwrap() error { return d.Cause }
func (l *Loaded) Err() error {
	errs := make([]error, len(l.Diagnostics))
	for i := range l.Diagnostics {
		errs[i] = l.Diagnostics[i]
	}
	return errors.Join(errs...)
}

// Default returns the built-in defaults.
func Default() Config {
	var c Config
	c.Source.SeedLines = 1000
	c.Detect.Enabled = true
	c.Detect.Cache = true
	c.FPM.Enabled = true
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
	if _, err := os.Stat("wstat.toml"); !errors.Is(err, os.ErrNotExist) {
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
func Load() *Loaded { return LoadWithPath("") }

// LoadWithPath overlays an explicit path after project/user settings. Unlike
// optional default files, a missing explicit file is an error.
func LoadWithPath(explicit string) *Loaded {
	l := &Loaded{Config: Default(), UserPath: UserConfigPath(), ProjectPath: ProjectConfigPath()}
	read := func(path string, optional bool) {
		fc, err := loadFile(path)
		if err != nil {
			if !optional || !errors.Is(err, os.ErrNotExist) {
				l.Diagnostics = append(l.Diagnostics, Diagnostic{Path: path, Cause: err})
			}
			return
		}
		overlay(&l.Config, fc, &l.Origins, path)
	}
	read(l.UserPath, true)
	if l.ProjectPath != "" {
		read(l.ProjectPath, false)
	}
	if explicit != "" {
		read(explicit, false)
	}
	return l
}

// ValidateFile checks an edited file independently of precedence overlays.
func ValidateFile(path string) error { _, err := loadFile(path); return err }

// fileConfig is a parsed config plus which keys were explicitly present
// (so that `detect.enabled = false` from a file wins over the default).
type fileConfig struct {
	cfg        Config
	hasEnabled bool
	hasCache   bool
	hasPaths   bool
	hasSeed    bool
	hasVhost   bool
	hasFPM     bool
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
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&fc.cfg); err != nil {
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
	if d, ok := raw["fpm"]; ok {
		_, fc.hasFPM = d["enabled"]
	}
	if fc.hasSeed && (fc.cfg.Source.SeedLines < 0 || fc.cfg.Source.SeedLines > 100000) {
		return nil, fmt.Errorf("seed_lines must be between 0 and 100000")
	}
	for _, p := range fc.cfg.Source.Paths {
		if p == "" {
			return nil, fmt.Errorf("source.paths contains an empty path")
		}
		if _, err := filepath.Match(p, ""); err != nil {
			return nil, fmt.Errorf("invalid source pattern %q: %w", p, err)
		}
	}
	for p := range fc.cfg.Source.Vhost {
		if _, err := filepath.Match(p, ""); err != nil {
			return nil, fmt.Errorf("invalid vhost pattern %q: %w", p, err)
		}
	}
	return fc, nil
}

// overlay applies explicitly-present fields from src onto dst, recording
// origins. Later overlays (project) win per field.
func overlay(dst *Config, src *fileConfig, o *Origins, origin string) {
	if src.hasPaths {
		dst.Source.Paths = src.cfg.Source.Paths
		o.Paths = origin
	}
	if src.hasSeed {
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
	if src.hasFPM {
		dst.FPM.Enabled = src.cfg.FPM.Enabled
		o.FPM = origin
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
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := Save(path, Default()); err != nil {
		return false, err
	}
	return false, nil
}
