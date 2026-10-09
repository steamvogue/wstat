package detect

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// nginxConfigPaths lists candidate nginx main configs, ordered by commonness.
var nginxConfigPaths = []string{
	"/etc/nginx/nginx.conf",
	"/usr/local/etc/nginx/nginx.conf", // FreeBSD/Homebrew
	"/opt/bitnami/nginx/conf/nginx.conf",
}

// ScanNginxConfig conservatively parses a nginx config tree: http-level
// access_log/log_format directives and server blocks with server_name +
// per-server access_log, following includes. Line-based, brace-depth
// tracked; same policy as the Apache scanner: skip and warn, never fail.
func ScanNginxConfig(mainConf string) *ScanResult {
	s := &scanner{
		baseDir: filepath.Dir(mainConf),
		vars:    map[string]string{},
		res: &ScanResult{
			LogFormats:  map[string]string{},
			VhostByLog:  map[string]string{},
			FormatByLog: map[string]string{},
		},
	}
	s.scanNginxFile(mainConf, 0)
	return s.res
}

func (s *scanner) scanNginxFile(path string, depth int) {
	if depth > maxIncludeDepth || s.filesScanned >= maxIncludeFiles {
		s.warn("include limit reached at " + path)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.warn("cannot read " + path)
		return
	}
	defer func() { _ = f.Close() }()
	s.filesScanned++

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s.scanNginxLine(line, depth)
	}
}

func (s *scanner) scanNginxLine(line string, depth int) {
	stmt := strings.TrimSuffix(line, ";")
	tokens := tokenize(stmt)
	if len(tokens) == 0 {
		return
	}
	enteringServer := tokens[0] == "server" && strings.Contains(stmt, "{") &&
		(len(tokens) == 1 || tokens[1] == "{")

	switch {
	case enteringServer:
		s.vhost = &VhostBlock{}
	case tokens[0] == "include" && len(tokens) >= 2:
		matches, _ := filepath.Glob(s.abs(tokens[1]))
		sort.Strings(matches)
		for _, m := range matches {
			if fi, err := os.Stat(m); err != nil || fi.IsDir() {
				continue
			}
			s.scanNginxFile(m, depth+1)
		}
	case tokens[0] == "log_format" && len(tokens) >= 2:
		s.res.LogFormats[tokens[1]] = strings.Join(tokens[2:], " ")
	case tokens[0] == "access_log" && len(tokens) >= 2:
		if tokens[1] == "off" || strings.HasPrefix(tokens[1], "syslog:") {
			break
		}
		ref := LogRef{Path: tokens[1]}
		if len(tokens) >= 3 {
			ref.Format = tokens[2]
		}
		if s.vhost != nil {
			s.vhost.Logs = append(s.vhost.Logs, ref)
		} else {
			s.res.GlobalLogs = append(s.res.GlobalLogs, ref)
		}
	case tokens[0] == "server_name" && s.vhost != nil:
		s.vhost.Aliases = append(s.vhost.Aliases, tokens[1:]...)
	}

	open := strings.Count(stmt, "{")
	close := strings.Count(stmt, "}")
	if enteringServer {
		// Entry depth = depth right inside the opening brace.
		s.serverEntry = s.braceDepth + open - close
		if s.serverEntry <= s.braceDepth {
			s.serverEntry = s.braceDepth + 1
		}
	}
	s.braceDepth += open - close
	if s.braceDepth < 0 {
		s.braceDepth = 0
	}
	if s.vhost != nil && !enteringServer && s.braceDepth < s.serverEntry {
		name := ""
		if len(s.vhost.Aliases) > 0 {
			name = s.vhost.Aliases[0]
		}
		s.res.Vhosts = append(s.res.Vhosts, *s.vhost)
		for _, l := range s.vhost.Logs {
			s.indexLog(l, name)
		}
		s.vhost = nil
	}
}
