// Package parser handles combined-family access log lines (Apache
// combined/vhost_combined, nginx combined and equivalents such as the
// Laravel Forge log format, which share the same layout).
package parser

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// Record is one parsed access-log request.
type Record struct {
	Vhost  string
	IP     string
	Method string
	Path   string
	Status int
	Bytes  int64
	Time   time.Time
	UA     string
	Bot    bool
	Static bool
	// LatencyUs is the request duration in microseconds when the source
	// format provides one (php-fpm access log %d); 0 otherwise.
	LatencyUs int64
}

var staticExts = map[string]struct{}{
	".css": {}, ".js": {}, ".mjs": {}, ".map": {}, ".png": {}, ".jpg": {},
	".jpeg": {}, ".gif": {}, ".webp": {}, ".svg": {}, ".ico": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".otf": {},
}

// IsStatic reports whether a URL path looks like a static asset.
func IsStatic(path string) bool {
	slash := strings.LastIndexByte(path, '/')
	dot := strings.LastIndexByte(path, '.')
	if dot < slash+1 {
		return false
	}
	_, ok := staticExts[strings.ToLower(path[dot:])]
	return ok
}

// ParseTimeToken parses an access-log timestamp token
// ("21/Oct/2025:14:26:43 +0200", offset optional). Exported for the php-fpm
// access-log parser, which shares the timestamp format.
func ParseTimeToken(s string) (time.Time, bool) { return parseTime(s) }

// SplitRequestLine splits a request line into method and path (query
// stripped, leading // normalized). Exported for the php-fpm parser.
func SplitRequestLine(req string) (method, path string) { return splitRequest(req) }

var botMarkers = []string{
	"bot", "spider", "crawler", "slurp", "curl/", "wget",
	"python-requests", "okhttp", "go-http-client", "facebookexternalhit",
	"headlesschrome", "monitoring", "zabbix", "uptime",
}

var quoteReplacer = strings.NewReplacer(`\"`, `"`, `\\`, `\`)

// Parse parses one access-log line. fallbackVhost is used when the line
// carries no vhost of its own (non-vhost_combined formats).
func Parse(line string, fallbackVhost string) (Record, bool) {
	var r Record
	if len(line) == 0 {
		return r, false
	}
	if c := line[len(line)-1]; c == '\r' {
		line = line[:len(line)-1]
	}
	if len(line) == 0 {
		return r, false
	}
	r.Vhost = fallbackVhost

	tok, rest := nextToken(line)
	if tok == "" {
		return r, false
	}
	if v, ok := vhostPrefix(tok); ok {
		r.Vhost = v
		tok, rest = nextToken(rest)
	}
	r.IP = tok
	// ident and remote user fields (unused but must be consumed)
	_, rest = nextToken(rest)
	_, rest = nextToken(rest)

	rest = trimLeftSpace(rest)
	if len(rest) < 3 || rest[0] != '[' {
		return r, false
	}
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return r, false
	}
	t, ok := parseTime(rest[1:end])
	if !ok {
		return r, false
	}
	r.Time = t
	rest = rest[end+1:]

	req, rest2, ok := readQuoted(rest)
	if !ok {
		return r, false
	}
	r.Method, r.Path = splitRequest(req)

	tok, rest3 := nextToken(rest2)
	st, err := strconv.Atoi(tok)
	if err != nil || st < 100 || st > 599 {
		return r, false
	}
	r.Status = st

	tok, rest4 := nextToken(rest3)
	if tok != "-" && tok != "" {
		if b, err := strconv.ParseInt(tok, 10, 64); err == nil && b > 0 {
			r.Bytes = b
		}
	}
	// Referer and User-Agent (both optional; tolerate either missing).
	if _, rr, ok := readQuoted(rest4); ok {
		if ua, _, ok2 := readQuoted(rr); ok2 {
			r.UA = ua
		}
	} else if ua, _, ok2 := readQuoted(rest4); ok2 {
		r.UA = ua
	}
	r.Bot = isBot(r.UA)
	r.Static = IsStatic(r.Path)
	return r, true
}

// Fingerprint returns the fraction of sample lines that parse.
func Fingerprint(lines []string) float64 {
	if len(lines) == 0 {
		return 0
	}
	ok := 0
	for _, l := range lines {
		if _, valid := Parse(l, "probe"); valid {
			ok++
		}
	}
	return float64(ok) / float64(len(lines))
}

// HasPrefixVhost reports whether the line starts with a vhost:port prefix
// as produced by Apache's vhost_combined format.
func HasPrefixVhost(line string) bool {
	tok, _ := nextToken(line)
	_, ok := vhostPrefix(tok)
	return ok
}

func isBot(ua string) bool {
	if len(ua) == 0 {
		return false
	}
	lu := strings.ToLower(ua)
	for _, m := range botMarkers {
		if strings.Contains(lu, m) {
			return true
		}
	}
	return false
}

func nextToken(s string) (tok, rest string) {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	j := i
	for j < len(s) && s[j] != ' ' {
		j++
	}
	return s[i:j], s[j:]
}

func trimLeftSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:]
}

// vhostPrefix reports whether tok is a "vhost:port" prefix as produced by
// Apache's vhost_combined format. The vhost may be a domain or an IP
// literal (default vhosts log their ServerName, which can be an IP);
// IPv6 client addresses never match because they contain several colons.
func vhostPrefix(tok string) (string, bool) {
	i := strings.LastIndexByte(tok, ':')
	if i <= 0 || i == len(tok)-1 {
		return "", false
	}
	if strings.Count(tok, ":") != 1 {
		return "", false
	}
	for k := i + 1; k < len(tok); k++ {
		if tok[k] < '0' || tok[k] > '9' {
			return "", false
		}
	}
	return tok[:i], true
}

// readQuoted reads a "..." quoted field, honoring backslash escapes.
func readQuoted(s string) (val, rest string, ok bool) {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	if i >= len(s) || s[i] != '"' {
		return "", s, false
	}
	i++
	j := i
	for j < len(s) {
		if s[j] == '\\' {
			j += 2
			continue
		}
		if s[j] == '"' {
			raw := s[i:j]
			if strings.IndexByte(raw, '\\') >= 0 {
				return quoteReplacer.Replace(raw), s[j+1:], true
			}
			return raw, s[j+1:], true
		}
		j++
	}
	return "", s, false
}

func splitRequest(req string) (method, path string) {
	i := strings.IndexByte(req, ' ')
	if i <= 0 {
		return "-", truncatePath(req)
	}
	method = req[:i]
	rest := req[i+1:]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		path = rest[:j]
	} else {
		path = rest
	}
	if q := strings.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	// Normalize leading duplicate slashes (//sync.php == /sync.php) so
	// bot probes aggregate into one row.
	for len(path) > 1 && path[0] == '/' && path[1] == '/' {
		path = path[1:]
	}
	if path == "" {
		path = "/"
	}
	return method, truncatePath(path)
}

func truncatePath(p string) string {
	if len(p) > 220 {
		return p[:220]
	}
	return p
}

var monthIdx = map[string]time.Month{
	"Jan": time.January, "Feb": time.February, "Mar": time.March,
	"Apr": time.April, "May": time.May, "Jun": time.June,
	"Jul": time.July, "Aug": time.August, "Sep": time.September,
	"Oct": time.October, "Nov": time.November, "Dec": time.December,
}

var locCache sync.Map // "±hhmm" -> *time.Location

func zoneFor(off string) *time.Location {
	if v, ok := locCache.Load(off); ok {
		return v.(*time.Location)
	}
	sign := 1
	if len(off) > 0 && off[0] == '-' {
		sign = -1
	}
	h, m := 0, 0
	if len(off) >= 5 {
		h, _ = strconv.Atoi(off[1:3])
		m, _ = strconv.Atoi(off[3:5])
	}
	l := time.FixedZone(off, sign*(h*3600+m*60))
	locCache.Store(off, l)
	return l
}

// parseTime parses "21/Oct/2025:14:26:43 +0200" without time.Parse overhead.
func parseTime(s string) (time.Time, bool) {
	if len(s) < 20 {
		return time.Time{}, false
	}
	if s[2] != '/' || s[6] != '/' || s[11] != ':' || s[14] != ':' || s[17] != ':' {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(s[0:2])
	if err != nil {
		return time.Time{}, false
	}
	mon, ok := monthIdx[s[3:6]]
	if !ok {
		return time.Time{}, false
	}
	year, err := strconv.Atoi(s[7:11])
	if err != nil {
		return time.Time{}, false
	}
	hh, err := strconv.Atoi(s[12:14])
	if err != nil {
		return time.Time{}, false
	}
	mm, err := strconv.Atoi(s[15:17])
	if err != nil {
		return time.Time{}, false
	}
	ss, err := strconv.Atoi(s[18:20])
	if err != nil {
		return time.Time{}, false
	}
	zone := time.UTC
	if len(s) >= 25 && (s[20] == ' ' || s[20] == '+' || s[20] == '-') {
		zone = zoneFor(s[len(s)-5:])
	}
	return time.Date(year, mon, day, hh, mm, ss, 0, zone), true
}
