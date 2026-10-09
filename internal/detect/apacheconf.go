package detect

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LogRef is one CustomLog directive found in a config.
type LogRef struct {
	Path   string `json:"path"` // variable-expanded
	Format string `json:"format"`
	Piped  bool   `json:"piped"`
}

// VhostBlock is a <VirtualHost> with its log references.
type VhostBlock struct {
	ServerName string   `json:"server_name"`
	Aliases    []string `json:"aliases"`
	Logs       []LogRef `json:"logs"`
}

// ScanResult is the outcome of scanning a web server configuration tree.
type ScanResult struct {
	LogFormats      map[string]string `json:"log_formats"` // nickname -> format string
	Vhosts          []VhostBlock      `json:"vhosts"`
	GlobalLogs      []LogRef          `json:"global_logs"`
	ErrorLogs       []string          `json:"error_logs"`
	VhostByLog      map[string]string `json:"vhost_by_log"`  // abs log path -> ServerName
	FormatByLog     map[string]string `json:"format_by_log"` // abs log path -> nickname
	Warnings        []string          `json:"warnings"`
	Files           []string          `json:"files"`
	IncludePatterns []string          `json:"include_patterns"`
}

const (
	maxIncludeDepth = 32
	maxIncludeFiles = 512
)

type scanner struct {
	baseDir      string // ServerRoot: relative includes and paths resolve here
	vars         map[string]string
	res          *ScanResult
	filesScanned int
	vhost        *VhostBlock
	globalName   string
	serverEntry  int // nginx: brace depth at which the current server block opened
	braceDepth   int
}

// ScanApacheConfig conservatively parses an Apache config tree: Include /
// IncludeOptional with glob expansion, Define and ${VAR} expansion, and
// VirtualHost blocks, extracting CustomLog/ErrorLog/LogFormat directives.
// Unknown constructs are skipped and reported; it never fails hard.
func ScanApacheConfig(mainConf string, vars map[string]string) *ScanResult {
	s := &scanner{
		baseDir: filepath.Dir(mainConf),
		vars:    map[string]string{},
		res: &ScanResult{
			LogFormats:  map[string]string{},
			VhostByLog:  map[string]string{},
			FormatByLog: map[string]string{},
		},
	}
	for k, v := range vars {
		s.vars[k] = v
	}
	s.scanFile(mainConf, 0)
	// Global server name backs vhostless logs.
	if s.globalName != "" && len(s.res.GlobalLogs) > 0 {
		for _, l := range s.res.GlobalLogs {
			if l.Piped {
				continue
			}
			if p := s.abs(l.Path); p != "" && s.res.VhostByLog[p] == "" {
				s.res.VhostByLog[p] = s.globalName
			}
		}
	}
	return s.res
}

func (s *scanner) scanFile(path string, depth int) {
	if depth > maxIncludeDepth {
		s.warn("include depth limit reached at " + path)
		return
	}
	if s.filesScanned >= maxIncludeFiles {
		s.warn("include file limit reached at " + path)
		return
	}
	s.filesScanned++
	s.res.Files = append(s.res.Files, path)
	f, err := os.Open(path)
	if err != nil {
		s.warn("cannot read " + path)
		return
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var pending string // continuation accumulator
	for sc.Scan() {
		line := sc.Text()
		if pending != "" {
			line = pending + " " + strings.TrimSpace(line)
			pending = ""
		}
		if strings.HasSuffix(strings.TrimSpace(line), "\\") {
			pending = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line), "\\"))
			continue
		}
		s.scanLine(line, depth)
	}
}

func (s *scanner) scanLine(line string, depth int) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return
	}
	tokens := tokenize(trimmed)
	if len(tokens) == 0 {
		return
	}
	switch strings.ToLower(tokens[0]) {
	case "<virtualhost":
		s.vhost = &VhostBlock{}
	case "</virtualhost>":
		if s.vhost != nil {
			if s.vhost.ServerName != "" || len(s.vhost.Aliases) > 0 {
				s.res.Vhosts = append(s.res.Vhosts, *s.vhost)
				for _, l := range s.vhost.Logs {
					s.indexLog(l, s.vhostName())
				}
			} else {
				// vhost without ServerName: logs get filename attribution
				for _, l := range s.vhost.Logs {
					s.indexLog(l, "")
				}
				if len(s.vhost.Logs) > 0 {
					s.res.Vhosts = append(s.res.Vhosts, *s.vhost)
				}
			}
		}
		s.vhost = nil
	case "define":
		if len(tokens) >= 3 {
			s.vars[tokens[1]] = tokens[2]
		}
	case "include", "includeoptional":
		if len(tokens) < 2 {
			return
		}
		pattern := s.abs(tokens[1])
		s.res.IncludePatterns = append(s.res.IncludePatterns, pattern)
		matches, err := filepath.Glob(pattern)
		if err != nil {
			s.warn("bad include pattern " + pattern)
			return
		}
		if len(matches) == 0 {
			if strings.ToLower(tokens[0]) == "include" {
				s.warn("include not matched " + pattern)
			}
			return
		}
		sort.Strings(matches)
		for _, m := range matches {
			if fi, err := os.Stat(m); err != nil || fi.IsDir() {
				continue
			}
			s.scanFile(m, depth+1)
		}
	case "logformat":
		if len(tokens) >= 3 {
			s.res.LogFormats[tokens[len(tokens)-1]] = strings.Join(tokens[1:len(tokens)-1], " ")
		} else if len(tokens) == 2 {
			// unquoted format without nickname: unusable, note it
			s.warn("LogFormat without nickname: " + trimmed)
		}
	case "customlog", "transferlog":
		if len(tokens) < 2 {
			return
		}
		ref := LogRef{Path: s.expand(tokens[1])}
		if strings.HasPrefix(tokens[1], "|") {
			ref.Piped = true
		}
		if len(tokens) >= 3 && !strings.HasPrefix(tokens[2], "env=") &&
			strings.ToLower(tokens[0]) == "customlog" {
			ref.Format = tokens[2]
		}
		if s.vhost != nil {
			s.vhost.Logs = append(s.vhost.Logs, ref)
		} else {
			s.res.GlobalLogs = append(s.res.GlobalLogs, ref)
		}
	case "errorlog":
		if len(tokens) >= 2 {
			s.res.ErrorLogs = append(s.res.ErrorLogs, s.expand(tokens[1]))
		}
	case "servername":
		name := ""
		if len(tokens) >= 2 {
			name = tokens[1]
		}
		if s.vhost != nil {
			if s.vhost.ServerName == "" {
				s.vhost.ServerName = name
			}
		} else {
			s.globalName = name
		}
	case "serveralias":
		if s.vhost != nil && len(tokens) > 1 {
			s.vhost.Aliases = append(s.vhost.Aliases, tokens[1:]...)
		}
	}
}

func (s *scanner) vhostName() string {
	if s.vhost == nil {
		return ""
	}
	if s.vhost.ServerName != "" {
		return s.vhost.ServerName
	}
	if len(s.vhost.Aliases) > 0 {
		return s.vhost.Aliases[0]
	}
	return ""
}

// indexLog records a log reference's vhost and format under its absolute
// path.
func (s *scanner) indexLog(l LogRef, vhost string) {
	if l.Piped {
		return
	}
	p := s.abs(l.Path)
	if p == "" {
		return
	}
	if vhost != "" {
		s.res.VhostByLog[p] = vhost
	}
	if l.Format != "" {
		s.res.FormatByLog[p] = l.Format
	}
}

// abs expands variables and resolves relative paths against the ServerRoot.
func (s *scanner) abs(p string) string {
	p = s.expand(p)
	if p == "" || strings.HasPrefix(p, "|") {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = filepath.Join(s.baseDir, p)
	}
	return filepath.Clean(p)
}

// expand substitutes ${VAR} references using known vars.
func (s *scanner) expand(p string) string {
	for {
		i := strings.Index(p, "${")
		if i < 0 {
			return p
		}
		j := strings.IndexByte(p[i:], '}')
		if j < 0 {
			return p
		}
		j += i
		name := p[i+2 : j]
		v, ok := s.vars[name]
		if !ok {
			// Leave the reference; caller decides. Avoid infinite loops.
			s.warn("undefined config variable ${" + name + "}")
			return strings.Replace(p, "${"+name+"}", "", 1)
		}
		p = p[:i] + v + p[j+1:]
	}
}

func (s *scanner) warn(msg string) {
	s.res.Warnings = append(s.res.Warnings, msg)
}

// tokenize splits a config line on whitespace, honoring double quotes.
func tokenize(line string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == '\\' && i+1 < len(line):
			cur.WriteByte(line[i+1])
			i++
		case (c == ' ' || c == '\t') && !inQuote:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return tokens
}
