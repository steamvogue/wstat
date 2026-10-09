// Package detect probes the local host for web-server logging layout:
// platform, Apache/nginx configuration, log files and their formats,
// permissions, and rotation scheme. It never modifies the host.
package detect

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// Platform describes the host wstat runs on.
type Platform struct {
	Hostname  string `json:"hostname"`
	Arch      string `json:"arch"`
	DistroID  string `json:"distro_id"`
	Distro    string `json:"distro"` // PRETTY_NAME
	Container bool   `json:"container"`
	Systemd   bool   `json:"systemd"`
}

// ProbePlatform collects host identity from /etc and /proc (read-only).
func ProbePlatform() Platform {
	p := Platform{
		Arch:    runtime.GOARCH,
		Systemd: dirExists("/run/systemd/system"),
	}
	if h, err := os.Hostname(); err == nil {
		p.Hostname = h
	}
	if kv := parseOSRelease("/etc/os-release"); kv != nil {
		p.DistroID = kv["ID"]
		p.Distro = kv["PRETTY_NAME"]
		if p.Distro == "" {
			p.Distro = kv["NAME"]
		}
	}
	p.Container = fileExists("/.dockerenv") || cgroupMentionsContainer()
	return p
}

// parseOSRelease returns KEY=VALUE pairs from an os-release file.
func parseOSRelease(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		kv[k] = v
	}
	return kv
}

func cgroupMentionsContainer() bool {
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(data)
	for _, marker := range []string{"docker", "containerd", "kubepods", "lxc", "machine-rkt"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
