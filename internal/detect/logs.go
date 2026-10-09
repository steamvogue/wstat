package detect

import (
	"bufio"
	"compress/gzip"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"github.com/steamvogue/wstat/internal/parser"
)

// LogProbe describes one candidate access log: size, readability, guessed
// format and parse ratio over a small tail sample.
type LogProbe struct {
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	Empty    bool    `json:"empty"`
	Missing  bool    `json:"missing"`
	Readable bool    `json:"readable"`
	Reason   string  `json:"reason,omitempty"` // remediation when unreadable
	Format   string  `json:"format"`           // combined|vhost_combined|json|w3c|unknown
	Ratio    float64 `json:"ratio"`
	Lines    int     `json:"sampled_lines"`
}

// ProbeLog samples the tail of a log file and fingerprints it. Missing and
// empty files are reported, not fatal: Apache creates per-vhost files
// lazily, so they may legitimately not exist yet.
func ProbeLog(path string) LogProbe {
	p := LogProbe{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			p.Missing = true
			p.Reason = "declared in config but not created yet (expected on idle vhosts)"
		} else {
			p.Reason = "stat failed: " + err.Error()
		}
		return p
	}
	p.Size = fi.Size()
	if p.Size == 0 {
		p.Empty = true
		p.Readable = true
		return p
	}
	f, err := os.Open(path)
	if err != nil {
		p.Reason = permissionRemedy(path, fi)
		return p
	}
	defer func() { _ = f.Close() }()

	p.Readable = true
	var lines []string
	if isGzip(f) {
		lines = gzTailLines(f, 50)
	} else {
		lines = tailLines(f, 50)
	}
	p.Lines = len(lines)
	if len(lines) == 0 {
		p.Empty = true
		return p
	}
	p.Ratio = parser.Fingerprint(lines)
	p.Format = guessFormat(lines)
	return p
}

// isGzip reports whether the open file starts with the gzip magic bytes.
func isGzip(f *os.File) bool {
	var head [2]byte
	if _, err := f.ReadAt(head[:], 0); err != nil {
		return false
	}
	return head[0] == 0x1f && head[1] == 0x8b
}

// gzTailLines decompresses (bounded) and returns the last n lines.
func gzTailLines(f *os.File, n int) []string {
	if _, err := f.Seek(0, 0); err != nil {
		return nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil
	}
	defer func() { _ = zr.Close() }()
	ring := make([]string, n)
	cnt, head := 0, 0
	var total int
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if cnt < n {
			ring[cnt] = line
			cnt++
		} else {
			ring[head] = line
			head = (head + 1) % n
		}
		if total += len(line); total > 8<<20 {
			break // decompression bomb guard
		}
	}
	if cnt < n {
		return ring[:cnt]
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = ring[(head+i)%n]
	}
	return out
}

// tailLines reads up to n final complete lines without loading the file.
func tailLines(f *os.File, n int) []string {
	const chunk = 64 * 1024
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	size := fi.Size()
	var buf []byte
	off := size
	for off > 0 && len(buf) < chunk {
		step := off
		if step > chunk {
			step = chunk
		}
		off -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, off); err != nil {
			break
		}
		buf = append(b, buf...)
	}
	lines := strings.Split(string(buf), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func guessFormat(lines []string) string {
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if s == "" {
			continue
		}
		switch {
		case strings.HasPrefix(s, "{"):
			return "json"
		case strings.HasPrefix(s, "#") && strings.Contains(strings.ToLower(s), "fields"):
			return "w3c"
		}
		if parser.HasPrefixVhost(s) {
			return "vhost_combined"
		}
	}
	// No line prefix signal: rely on the parser ratio (combined-family).
	for _, l := range lines {
		if r, ok := parser.Parse(l, "probe"); ok && r.Status >= 100 {
			return "combined"
		}
	}
	return "unknown"
}

// permissionRemedy explains how to gain read access, distro-aware.
func permissionRemedy(path string, fi os.FileInfo) string {
	who := currentUser()
	owner := "unknown"
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10)); err == nil {
			owner = u.Username
		}
	}
	var sb strings.Builder
	sb.WriteString("no read permission (")
	sb.WriteString(modeString(fi.Mode()))
	sb.WriteString(", owner ")
	sb.WriteString(owner)
	sb.WriteString("); fix: ")
	plat := ProbePlatform()
	switch plat.DistroID {
	case "debian", "ubuntu", "raspbian":
		sb.WriteString("usermod -aG adm " + who + " (then re-login), or setfacl -m u:" + who + ":r " + path)
	case "rhel", "fedora", "rocky", "almalinux", "centos":
		sb.WriteString("sudo setfacl -m u:" + who + ":r " + path + " (RHEL httpd logs are often root-only)")
	default:
		sb.WriteString("add " + who + " to the log-owning group, or setfacl -m u:" + who + ":r " + path)
	}
	return sb.String()
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "$USER"
}

func modeString(m os.FileMode) string { return m.String() }
