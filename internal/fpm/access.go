package fpm

import (
	"strconv"
	"strings"
	"time"

	"github.com/steamvogue/wstat/internal/parser"
)

// ParseAccess parses one php-fpm access.log line into a store record.
// attribution is used as the record vhost (the pool name). php-fpm renders
// %d as fractional seconds on PHP >= 8 (e.g. "0.002"); older builds logged
// microseconds — both are handled by magnitude sanity.
func ParseAccess(line string, attribution string) (parser.Record, bool) {
	var r parser.Record
	if len(line) == 0 {
		return r, false
	}
	fields := splitAccessLine(line)
	if len(fields) < 5 {
		return r, false
	}
	r.Vhost = attribution
	// %R: client IP first (may be "-").
	r.IP = fields[0]

	// Find the bracketed-or-bare timestamp: token matching dd/Mon/yyyy:hh:mm:ss +zzzz
	ti := -1
	for i, f := range fields {
		if len(f) >= 20 && f[2] == '/' && f[6] == '/' && f[11] == ':' &&
			i+1 < len(fields) && len(fields[i+1]) >= 5 &&
			(fields[i+1][0] == '+' || fields[i+1][0] == '-') {
			ti = i
			break
		}
	}
	if ti < 0 {
		return r, false
	}
	ts, ok := parser.ParseTimeToken(fields[ti] + " " + fields[ti+1])
	if !ok {
		return r, false
	}
	r.Time = ts

	// Quoted request: "METHOD URI" spanning tokens 3..n-1 of the remainder.
	q := ti + 2
	if q >= len(fields) || !strings.HasPrefix(fields[q], "\"") {
		return r, false
	}
	var reqTokens []string
	for i := q; i < len(fields); i++ {
		reqTokens = append(reqTokens, fields[i])
		if strings.HasSuffix(fields[i], "\"") {
			req := strings.Trim(strings.Join(reqTokens, " "), "\"")
			r.Method, r.Path = parser.SplitRequestLine(req)
			q = i + 1
			break
		}
	}
	if q >= len(fields) {
		return r, false
	}

	// Status, then trailing numeric fields (%d duration, %M memory, …).
	st, err := strconv.Atoi(fields[q])
	if err != nil || st < 100 || st > 599 {
		return r, false
	}
	r.Status = st
	q++
	var nums []float64
	for ; q < len(fields); q++ {
		f := strings.TrimSuffix(fields[q], "s")
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			// tolerate a unit-suffixed duration like "12ms"
			if len(fields[q]) > 2 {
				if v, err = strconv.ParseFloat(strings.TrimRight(fields[q], "msu"), 64); err != nil {
					break
				}
			} else {
				break
			}
		}
		nums = append(nums, v)
	}
	if len(nums) >= 1 {
		r.LatencyUs = normalizeDurationToUs(nums[0])
	}
	if len(nums) >= 2 && nums[1] > 0 && nums[1] < 1<<32 {
		r.Bytes = int64(nums[1]) // %M: last-request memory footprint in bytes
	}
	return r, true
}

// normalizeDurationToUs maps php-fpm %d renderings to microseconds: PHP >= 8
// logs fractional seconds ("0.002"), older builds logged microseconds
// ("2000"). Values below 10 are treated as seconds.
func normalizeDurationToUs(v float64) int64 {
	if v > 0 && v < 10 {
		return int64(v * 1e6)
	}
	return int64(v)
}

// splitAccessLine splits on spaces, honoring double quotes.
func splitAccessLine(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(line) && inQuote:
			cur.WriteByte(line[i+1])
			i++
		case (c == ' ' || c == '\t') && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
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
