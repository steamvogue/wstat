package detect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steamvogue/wstat/internal/logsrc"
)

// Report is the full host-detection result. JSON-serializable for
// `wstat detect --json` (fleet prep / automation).
type Report struct {
	Generated time.Time   `json:"generated"`
	Platform  Platform    `json:"platform"`
	Apache    Apache      `json:"apache"`
	NginxConf string      `json:"nginx_conf"`
	Scan      *ScanResult `json:"apache_scan,omitempty"`
	NginxScan *ScanResult `json:"nginx_scan,omitempty"`
	Rotation  Rotation    `json:"rotation"`
	Probes    []LogProbe  `json:"probes"`
	Warnings  []string    `json:"warnings"`
}

// Run probes the host end-to-end. It never modifies anything and always
// returns a usable report.
func Run() *Report {
	rep := &Report{Generated: time.Now()}
	rep.Platform = ProbePlatform()
	rep.Rotation = ProbeRotation()
	rep.Apache = ProbeApache()

	// Candidate log paths, most authoritative first.
	candidates := map[string]bool{}

	if rep.Apache.Found {
		vars := map[string]string{}
		if envvars := EnvvarsFile(rep.Apache.ConfigRoot); envvars != "" {
			vars = ParseEnvvars(envvars)
		}
		rep.Scan = ScanApacheConfig(rep.Apache.ConfigFile, vars)
		if p := EnvvarsFile(rep.Apache.ConfigRoot); p != "" {
			rep.Scan.Files = append(rep.Scan.Files, p)
		}
		rep.Warnings = append(rep.Warnings, rep.Scan.Warnings...)
		for p := range rep.Scan.VhostByLog {
			candidates[p] = true
		}
		for _, l := range rep.Scan.GlobalLogs {
			if p := normalizeLogPath(l.Path, rep.Apache.ConfigRoot); p != "" && !l.Piped {
				candidates[p] = true
			}
		}
	}

	for _, p := range nginxConfigPaths {
		if !fileExists(p) {
			continue
		}
		rep.NginxConf = p
		break
	}
	if rep.NginxConf != "" {
		rep.NginxScan = ScanNginxConfig(rep.NginxConf)
		rep.Warnings = append(rep.Warnings, rep.NginxScan.Warnings...)
		for p := range rep.NginxScan.VhostByLog {
			candidates[p] = true
		}
		for _, l := range rep.NginxScan.GlobalLogs {
			if p := normalizeLogPath(l.Path, ""); p != "" && !l.Piped {
				candidates[p] = true
			}
		}
	}

	// Fall back to the standard globs for anything the config didn't cover
	// (or when neither server was found).
	for _, s := range logsrc.Discover(nil, nil) {
		candidates[s.Path] = true
	}

	paths := make([]string, 0, len(candidates))
	for p := range candidates {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		rep.Probes = append(rep.Probes, ProbeLog(p))
	}
	return rep
}

// normalizeLogPath expands pipes away (piped logs are unusable) and resolves
// relative paths against the server root.
func normalizeLogPath(p, root string) string {
	if strings.HasPrefix(p, "|") {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		if root == "" {
			return p
		}
		return root + "/" + p
	}
	return p
}

// VhostMap returns path → vhost from the config scans (exact attribution).
func (r *Report) VhostMap() map[string]string {
	m := map[string]string{}
	for _, scan := range []*ScanResult{r.Scan, r.NginxScan} {
		if scan == nil {
			continue
		}
		for p, v := range scan.VhostByLog {
			m[p] = v
		}
	}
	return m
}

// Sources converts the report into tail-ready sources for runtime use:
// existing readable files whose tail sample parses as combined-family (or
// that are empty/missing-but-configured, which the tailer waits for).
func (r *Report) Sources() []logsrc.Source {
	vhostMap := r.VhostMap()
	var out []logsrc.Source
	for _, p := range r.Probes {
		base := baseOf(p.Path)
		if p.Missing || p.Empty {
			// Only wait for files the config actually declares.
			if _, declared := vhostMap[p.Path]; !declared {
				continue
			}
			out = append(out, logsrc.Source{
				Path:  p.Path,
				Vhost: vhostMap[p.Path],
			})
			continue
		}
		if !p.Readable || p.Ratio < 0.5 || p.Format == "json" || p.Format == "w3c" {
			continue
		}
		vhost := vhostMap[p.Path]
		if vhost == "" {
			vhost = logsrc.VhostFromFilename(base)
		}
		out = append(out, logsrc.Source{
			Path:   p.Path,
			Vhost:  vhost,
			Replay: logsrc.IsReplay(base),
		})
	}
	return out
}

func baseOf(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

// Doctor renders the human-readable report for `wstat doctor`.
func (r *Report) Doctor() string {
	var b strings.Builder
	fmt.Fprintf(&b, "wstat doctor — host detection report (%s)\n", r.Generated.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "host:        %s (%s, %s", r.Platform.Hostname, r.Platform.Distro, r.Platform.Arch)
	if r.Platform.Container {
		b.WriteString(", container")
	}
	b.WriteString(")\n")

	if r.Apache.Found {
		fmt.Fprintf(&b, "apache:      %s via %s\n", r.Apache.Version, r.Apache.Control)
		fmt.Fprintf(&b, "config:      %s\n", r.Apache.ConfigFile)
		if r.Scan != nil {
			fmt.Fprintf(&b, "vhosts:      %d with custom logs; %d log formats defined\n",
				len(r.Scan.Vhosts), len(r.Scan.LogFormats))
		}
		if len(r.Apache.Modules) > 0 {
			status := ""
			for _, m := range r.Apache.Modules {
				if m == "status" {
					status = " (mod_status loaded)"
				}
			}
			fmt.Fprintf(&b, "modules:     %d loaded%s\n", len(r.Apache.Modules), status)
		}
	} else {
		b.WriteString("apache:      not found\n")
	}
	if r.NginxConf != "" {
		fmt.Fprintf(&b, "nginx:       config %s (%d server blocks with logs)\n",
			r.NginxConf, len(r.NginxScan.Vhosts))
	}
	if r.Rotation.Found {
		fmt.Fprintf(&b, "rotation:    %s, keep %d, compress=%v copytruncate=%v (%s)\n",
			r.Rotation.Schedule, r.Rotation.Keep, r.Rotation.Compress, r.Rotation.CopyTruncate,
			r.Rotation.ConfigPath)
	} else {
		b.WriteString("rotation:    no logrotate config found for apache2/httpd/nginx\n")
	}

	if len(r.Probes) > 0 {
		fmt.Fprintf(&b, "log candidates (%d):\n", len(r.Probes))
		for _, p := range r.Probes {
			fmt.Fprintf(&b, "  %s %-40s ", statusMark(p), p.Path)
			switch {
			case p.Missing:
				b.WriteString("missing (declared, will be waited on)")
			case p.Empty:
				b.WriteString("empty (picked up when written)")
			case !p.Readable:
				b.WriteString("unreadable")
			default:
				fmt.Fprintf(&b, "%-15s parses %.0f%% of %d sample lines",
					p.Format, p.Ratio*100, p.Lines)
			}
			if v := r.VhostMap()[p.Path]; v != "" {
				fmt.Fprintf(&b, " vhost=%s (config)", v)
			}
			b.WriteString("\n")
			if !p.Readable && p.Reason != "" {
				fmt.Fprintf(&b, "      ! %s\n", p.Reason)
			}
		}
	}

	if srcs := r.Sources(); len(srcs) > 0 {
		live, replay := 0, 0
		for _, s := range srcs {
			if s.Replay {
				replay++
			} else {
				live++
			}
		}
		fmt.Fprintf(&b, "tailable:    %d sources (%d live, %d replay)\n", len(srcs), live, replay)
	} else {
		b.WriteString("tailable:    none — pass explicit globs, e.g. wstat '/var/log/*access*.log'\n")
	}

	if len(r.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "  - %s\n", w)
		}
	}
	return b.String()
}

func statusMark(p LogProbe) string {
	switch {
	case p.Missing, p.Empty:
		return "·"
	case !p.Readable:
		return "✗"
	default:
		return "✓"
	}
}

// JSON renders the report for `wstat detect --json`.
func (r *Report) JSON() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return `{"error":"marshal failed"}`
	}
	return string(data)
}
