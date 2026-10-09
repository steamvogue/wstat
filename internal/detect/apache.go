package detect

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Apache describes the detected Apache httpd installation.
type Apache struct {
	Found      bool     `json:"found"`
	Control    string   `json:"control_binary"` // apache2ctl / apachectl / httpd
	Version    string   `json:"version"`
	ConfigRoot string   `json:"config_root"` // HTTPD_ROOT
	ConfigFile string   `json:"config_file"` // resolved main config (absolute)
	Modules    []string `json:"modules"`     // best-effort from -M
}

var (
	reServerVersion = regexp.MustCompile(`Server version:\s*(\S.*)`)
	reHTTPDRoot     = regexp.MustCompile(`-D HTTPD_ROOT="([^"]+)"`)
	reServerConfig  = regexp.MustCompile(`-D SERVER_CONFIG_FILE="([^"]+)"`)
	reModuleLine    = regexp.MustCompile(`^\s+(\w+)_module\s+\((?:static|shared)\)`)
)

// ProbeApache locates an Apache control binary and reads its compile-time
// settings via -V (works unprivileged even when the config can't be fully
// loaded). Module detection via -M is best-effort with a short timeout.
func ProbeApache() Apache {
	var a Apache
	for _, name := range []string{"apache2ctl", "apachectl", "apache2", "httpd"} {
		bin, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		out, _ := runProbe(bin, "-V")
		if m := reServerVersion.FindStringSubmatch(out); m == nil {
			continue
		}
		a.Found = true
		a.Control = bin
		a.Version = strings.TrimSpace(reServerVersion.FindStringSubmatch(out)[1])
		if m := reHTTPDRoot.FindStringSubmatch(out); m != nil {
			a.ConfigRoot = m[1]
		}
		if m := reServerConfig.FindStringSubmatch(out); m != nil {
			cfg := m[1]
			if a.ConfigRoot != "" && !strings.HasPrefix(cfg, "/") {
				cfg = a.ConfigRoot + "/" + cfg
			}
			a.ConfigFile = cfg
		}
		if mods, ok := probeModules(bin); ok {
			a.Modules = mods
		}
		return a
	}
	return a
}

func probeModules(bin string) ([]string, bool) {
	out, ok := runProbe(bin, "-M")
	if !ok {
		return nil, false
	}
	var mods []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if m := reModuleLine.FindStringSubmatch(sc.Text()); m != nil {
			mods = append(mods, m[1])
		}
	}
	return mods, mods != nil
}

// runProbe executes a short-lived probe command, returning combined output.
// A non-zero exit still yields parseable output (e.g. raw `apache2 -V`
// complains about undefined env vars but prints compile-time settings).
func runProbe(bin string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", false
	}
	return string(out), err == nil || true
}

// EnvvarsFile returns the distro envvars file path (Debian/Ubuntu) that
// defines APACHE_LOG_DIR and friends, if readable.
func EnvvarsFile(configRoot string) string {
	for _, p := range []string{configRoot + "/envvars", "/etc/apache2/envvars"} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// ParseEnvvars reads an Apache envvars file: `export VAR=value` lines.
// Values may reference other variables with $VAR or ${VAR} (Debian uses
// APACHE_LOG_DIR=/var/log/apache2$SUFFIX); those are expanded using earlier
// definitions and the process environment, defaulting to empty.
func ParseEnvvars(path string) map[string]string {
	vars := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return vars
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		vars[strings.TrimSpace(k)] = expandShellVars(strings.Trim(strings.TrimSpace(v), `"'`), vars)
	}
	return vars
}

// expandShellVars substitutes $VAR and ${VAR} references (braced first, then
// longest-name prefix matches).
func expandShellVars(s string, vars map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		if s[i+1] == '{' {
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				name := s[i+2 : i+end]
				if v, ok := vars[name]; ok {
					b.WriteString(v)
				} else if ev, ok := os.LookupEnv(name); ok {
					b.WriteString(ev)
				}
				i += end
				continue
			}
		}
		// Unbraced: longest identifier match.
		j := i + 1
		for j < len(s) && (isIdentByte(s[j])) {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			continue
		}
		name := s[i+1 : j]
		if v, ok := vars[name]; ok {
			b.WriteString(v)
		} else if ev, ok := os.LookupEnv(name); ok {
			b.WriteString(ev)
		}
		i = j - 1
	}
	return b.String()
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
