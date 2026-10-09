package fpm

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/steamvogue/wstat/internal/parser"
)

// AccessRecord is a PHP service event. Request memory has its own unit and
// cannot be passed to the web store as traffic bytes.
type AccessRecord struct {
	Pool, IP, Method, Path     string
	Time                       time.Time
	Status                     int
	LatencyUs, MemoryBytes     int64
	DurationKnown, MemoryKnown bool
}

const DefaultAccessFormat = `%R - %u %t "%m %r" %s`
const durationAccessFormat = DefaultAccessFormat + ` %d %M`

type accessField struct {
	name byte
	unit string
}
type AccessParser struct {
	re     *regexp.Regexp
	fields []accessField
}

// NewAccessParser compiles the configured layout once, with explicit units.
// Custom strftime timestamps and unknown placeholders are rejected rather than
// interpreted as a different layout. See PHP's FPM configuration manual.
func NewAccessParser(format string) (*AccessParser, error) {
	if format == "" {
		format = DefaultAccessFormat
	}
	if len(format) > 16384 {
		return nil, fmt.Errorf("FPM access format exceeds 16 KiB")
	}
	var pattern strings.Builder
	pattern.WriteByte('^')
	var fields []accessField
	for i := 0; i < len(format); {
		if format[i] == ' ' || format[i] == '\t' {
			for i < len(format) && (format[i] == ' ' || format[i] == '\t') {
				i++
			}
			pattern.WriteString(`[ \t]+`)
			continue
		}
		if format[i] != '%' {
			pattern.WriteString(regexp.QuoteMeta(format[i : i+1]))
			i++
			continue
		}
		i++
		if i >= len(format) {
			return nil, fmt.Errorf("unfinished FPM placeholder")
		}
		if format[i] == '%' {
			pattern.WriteByte('%')
			i++
			continue
		}
		unit := ""
		if format[i] == '{' {
			end := strings.IndexByte(format[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unfinished FPM unit")
			}
			unit = format[i+1 : i+end]
			i += end + 1
		}
		if i >= len(format) {
			return nil, fmt.Errorf("unfinished FPM placeholder")
		}
		name := format[i]
		i++
		// Treat the standard URI/query sequence as one URI capture.
		if name == 'r' && strings.HasPrefix(format[i:], "%Q%q") {
			i += 4
		}
		if i < len(format) && format[i] == '%' && !strings.HasPrefix(format[i:], "%%") {
			return nil, fmt.Errorf("adjacent FPM fields require a delimiter")
		}
		if unit != "" && name != 'd' && name != 'M' && name != 't' && name != 'T' {
			return nil, fmt.Errorf("unit on FPM %%%c unsupported", name)
		}
		fragment := `.*?`
		switch name {
		case 't', 'T':
			if unit != "" {
				return nil, fmt.Errorf("custom FPM timestamp layouts are unsupported")
			}
			fragment = `[0-9]{2}/[A-Za-z]{3}/[0-9]{4}:[0-9]{2}:[0-9]{2}:[0-9]{2} [+-][0-9]{4}`
		case 'd':
			if _, ok := durationScale(unit); !ok {
				return nil, fmt.Errorf("unsupported FPM duration unit %q", unit)
			}
			fragment = `[0-9]+(?:\.[0-9]+)?`
		case 'M':
			if _, ok := memoryScale(unit); !ok {
				return nil, fmt.Errorf("unsupported FPM memory unit %q", unit)
			}
			fragment = `[0-9]+(?:\.[0-9]+)?`
		case 's', 'p', 'P', 'l':
			fragment = `[0-9]+`
		case 'R', 'm', 'n':
			fragment = `[^ \t]+`
		case 'u', 'r', 'q', 'Q', 'f':
			if unit != "" {
				return nil, fmt.Errorf("unit on FPM %%%c unsupported", name)
			}
		default:
			return nil, fmt.Errorf("unsupported FPM placeholder %%%c", name)
		}
		fields = append(fields, accessField{name: name, unit: unit})
		pattern.WriteByte('(')
		pattern.WriteString(fragment)
		pattern.WriteByte(')')
	}
	pattern.WriteByte('$')
	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("FPM format has no fields")
	}
	return &AccessParser{re: re, fields: fields}, nil
}

// AccessParsers prevents ambiguous attribution of shared files and conflicting
// layouts. Shared logs require %n to identify each request's pool.
func AccessParsers(pools []Pool) (map[string]*AccessParser, map[string]string) {
	groups := map[string][]Pool{}
	for _, p := range pools {
		if p.AccessLog != "" {
			groups[p.AccessLog] = append(groups[p.AccessLog], p)
		}
	}
	parsers := map[string]*AccessParser{}
	diagnostics := map[string]string{}
	for path, group := range groups {
		format := group[0].AccessFormat
		conflict := false
		for _, p := range group {
			if p.AccessFormat != format {
				conflict = true
			}
		}
		if conflict {
			diagnostics[path] = "shared FPM log has conflicting access formats"
			continue
		}
		ap, err := NewAccessParser(format)
		if err != nil {
			diagnostics[path] = err.Error()
			continue
		}
		if len(group) > 1 {
			hasPool := false
			for _, f := range ap.fields {
				if f.name == 'n' {
					hasPool = true
				}
			}
			if !hasPool {
				diagnostics[path] = "shared FPM log requires %n for pool attribution"
				continue
			}
		}
		parsers[path] = ap
	}
	return parsers, diagnostics
}
func durationScale(unit string) (float64, bool) {
	switch unit {
	case "", "seconds":
		return 1e6, true
	case "milliseconds", "milli":
		return 1000, true
	case "microseconds", "micro":
		return 1, true
	}
	return 0, false
}
func memoryScale(unit string) (float64, bool) {
	switch unit {
	case "", "bytes":
		return 1, true
	case "kilobytes", "kilo":
		return 1024, true
	case "megabytes", "mega":
		return 1048576, true
	}
	return 0, false
}
func scaledNumber(text string, scale float64) (int64, bool) {
	v, err := strconv.ParseFloat(text, 64)
	v *= scale
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(math.Round(v)), true
}
func (p *AccessParser) Parse(line, pool string) (AccessRecord, bool) {
	r := AccessRecord{Pool: pool}
	parts := p.re.FindStringSubmatch(line)
	if parts == nil {
		return r, false
	}
	for i, f := range p.fields {
		text := parts[i+1]
		switch f.name {
		case 'R':
			r.IP = text
		case 'n':
			r.Pool = text
		case 'm':
			r.Method = text
		case 'r':
			r.Path = text
			if j := strings.IndexByte(r.Path, '?'); j >= 0 {
				r.Path = r.Path[:j]
			}
		case 't', 'T':
			v, ok := parser.ParseTimeToken(text)
			if !ok {
				return r, false
			}
			r.Time = v
		case 's':
			v, err := strconv.Atoi(text)
			if err != nil || v < 100 || v > 599 {
				return r, false
			}
			r.Status = v
		case 'd':
			scale, _ := durationScale(f.unit)
			v, ok := scaledNumber(text, scale)
			if !ok {
				return r, false
			}
			r.LatencyUs = v
			r.DurationKnown = true
		case 'M':
			scale, _ := memoryScale(f.unit)
			v, ok := scaledNumber(text, scale)
			if !ok {
				return r, false
			}
			r.MemoryBytes = v
			r.MemoryKnown = true
		}
	}
	return r, true
}

// ParseAccess is the conventional layout helper. Runtime ingestion always
// uses NewAccessParser(pool.AccessFormat), so it never guesses units/layouts.
func ParseAccess(line, pool string) (AccessRecord, bool) {
	p, _ := NewAccessParser(durationAccessFormat)
	return p.Parse(line, pool)
}

// SlowlogEntry is one parsed slowlog block (timestamp + pool + stack head).
type SlowlogEntry struct {
	Time time.Time
	Pool string
	Head string
}

// ParseSlowlogHead extracts the summary line of a slowlog block.
func ParseSlowlogHead(line string) (SlowlogEntry, bool) {
	// Format: [09-Oct-2026 19:30:12]  [pool www] pid 12345
	//         script_filename = /x.php
	if !strings.HasPrefix(line, "[") {
		return SlowlogEntry{}, false
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return SlowlogEntry{}, false
	}
	ts, err := time.Parse("02-Jan-2006 15:04:05", strings.Trim(line[1:end], " "))
	if err != nil {
		return SlowlogEntry{}, false
	}
	rest := line[end+1:]
	pool := ""
	if m := strings.Index(rest, "[pool "); m >= 0 {
		if e := strings.Index(rest[m:], "]"); e > 0 {
			pool = rest[m+len("[pool ") : m+e]
		}
	}
	return SlowlogEntry{Time: ts, Pool: pool, Head: strings.TrimSpace(rest)}, true
}
