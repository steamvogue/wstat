package detect

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Rotation summarizes the logrotate scheme governing the web server logs.
type Rotation struct {
	Found        bool   `json:"found"`
	ConfigPath   string `json:"config_path"`
	Schedule     string `json:"schedule"` // daily/weekly/monthly
	Keep         int    `json:"keep"`     // rotate N
	Compress     bool   `json:"compress"`
	CopyTruncate bool   `json:"copytruncate"`
}

// ProbeRotation reads the distro logrotate snippet for the web server, if
// present.
func ProbeRotation() Rotation {
	for _, daemon := range []string{"apache2", "httpd", "nginx"} {
		p := filepath.Join("/etc/logrotate.d", daemon)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		r := parseRotationConfig(data)
		r.ConfigPath = p
		return r
	}
	return Rotation{}
}

// parseRotationConfig extracts the rotation scheme from a logrotate snippet.
func parseRotationConfig(data []byte) Rotation {
	var r Rotation
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "daily", "weekly", "monthly", "yearly":
			r.Found = true
			r.Schedule = f[0]
		case "rotate":
			r.Found = true
			if n, err := strconv.Atoi(f[1]); err == nil {
				r.Keep = n
			}
		case "compress":
			r.Compress = true
		case "nocompress":
			r.Compress = false
		case "copytruncate":
			r.CopyTruncate = true
		}
	}
	return r
}
