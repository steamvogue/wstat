// Package logsrc discovers access logs, attributes vhosts from filenames,
// and tails them rotation-safely (Apache combined-family formats only).
package logsrc

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steamvogue/wstat/internal/filetail"
)

// Source is one tailed access log file.
type Source struct {
	Path   string
	Vhost  string // fallback vhost derived from the filename
	Replay bool   // rotated/compressed history: replay once, never tail
	Kind   Kind
	Format string // service access format; ignored for web sources
}

// Kind determines ingestion ownership; service events never enter web totals.
type Kind uint8

const (
	Web Kind = iota
	FPM
)

// IsReplay recognizes numbered/date rotations and compressed history. Custom
// active filenames, including access_log, remain live.
var replaySuffix = regexp.MustCompile(`(?:\.[0-9]+|[-.]\d{4}-?\d{2}-?\d{2})(?:\.gz)?$|\.gz$`)

func IsReplay(path string) bool { return replaySuffix.MatchString(filepath.Base(path)) }

// DefaultGlobs covers common Apache/nginx layouts (Debian, RHEL, nginx/Forge)
// plus their rotated variants (replayed as history).
var DefaultGlobs = []string{
	"/var/log/apache2/*access*.log",
	"/var/log/apache2/*access*.log.[0-9]*",
	"/var/log/httpd/*access*log*",
	"/var/log/nginx/*access*.log",
	"/var/log/nginx/*access*.log.[0-9]*",
}

var errorLogRe = regexp.MustCompile(`error`)

// rotationSuffix strips logrotate suffixes: ".log.12.gz" -> ".log".
var rotationSuffix = regexp.MustCompile(`(\.[0-9]+)?(\.gz)?$`)

var vhostPatterns = []*regexp.Regexp{
	// <vhost>-access.log / <vhost>-ssl-access.log / <vhost>_access.log
	// (cPanel-style per-domain logs: port 80 and 443 merge into one vhost)
	regexp.MustCompile(`^(.+?)(?:[-_]ssl)?[-_]access[-_.]?log$`),
	// <vhost>.access.log (Forge-style)
	regexp.MustCompile(`^(.+?)\.access\.log$`),
}

// RawLine is one physical log line with its source attribution.
type RawLine struct {
	Text   string
	Source *Source
	Seeded bool // true when replayed from history at startup
}

// Discover finds regular access-log files matching the globs
// (DefaultGlobs when nil). Error logs are skipped. vhostMap (path → vhost,
// from the detection config scan) overrides filename attribution. Files
// with recognized rotation/compression suffixes are Replay. Active custom
// filenames are live. Sorted by path.
func Discover(globs []string, vhostMap map[string]string) []Source {
	if len(globs) == 0 {
		globs = DefaultGlobs
	}
	seen := map[string]bool{}
	var out []Source
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			continue
		}
		if len(matches) == 0 && !strings.ContainsAny(g, "*?[") {
			matches = []string{g}
		}
		for _, p := range matches {
			if seen[p] {
				continue
			}
			seen[p] = true
			base := filepath.Base(p)
			if errorLogRe.MatchString(base) {
				continue
			}
			fi, err := os.Stat(p)
			if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !fi.Mode().IsRegular() {
				continue
			}
			vhost := VhostFromFilename(base)
			if v2 := MatchVhostPin(vhostMap, p); v2 != "" {
				vhost = v2
			}
			out = append(out, Source{
				Path:   p,
				Vhost:  vhost,
				Replay: IsReplay(base),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// matchVhostPin resolves a path against the vhost map: exact match first,
// then glob pins (e.g. "/var/log/apache2/x-access.log*" covers rotated
// variants of the same vhost).
func MatchVhostPin(vhostMap map[string]string, path string) string {
	if len(vhostMap) == 0 {
		return ""
	}
	if v, ok := vhostMap[path]; ok && v != "" {
		return v
	}
	keys := make([]string, 0, len(vhostMap))
	for k := range vhostMap {
		if strings.ContainsAny(k, "*?[") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ok, err := filepath.Match(k, path); err == nil && ok {
			return vhostMap[k]
		}
	}
	return ""
}

func vhostFromFilename(base string) string { return VhostFromFilename(base) }

// VhostFromFilename maps a log filename to its vhost, tolerating logrotate
// suffixes (".log.12.gz"): strips rotation suffixes, applies the
// <vhost>-access.log / <vhost>-ssl-access.log / <vhost>.access.log patterns
// (443 variants merge into the domain) and maps plain access logs to
// "default". Exported for the detection package.
func VhostFromFilename(base string) string {
	base = rotationSuffix.ReplaceAllString(base, "")
	for _, re := range vhostPatterns {
		if m := re.FindStringSubmatch(base); len(m) > 1 && m[1] != "" {
			return m[1]
		}
	}
	trimmed := strings.TrimSuffix(base, filepath.Ext(base))
	if trimmed == "" || trimmed == "access" {
		return "default"
	}
	return trimmed
}

// seedLines reads up to the last maxLines complete lines of the file and
// returns the byte offset where live tailing must resume so that no line is
// lost or double-counted. Gzip files are fully decompressed (bounded) and
// only their tail is kept; their offset is irrelevant for replay sources.
// ReadSeed returns bounded history in deterministic file order for diagnostics.
func ReadSeed(path string, maxLines int) ([]string, error) {
	f, err := filetail.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	lines, _, _, err := seedFile(f, maxLines)
	return lines, err
}

func seedLines(path string, maxLines int) ([]string, int64) {
	f, err := filetail.Open(path)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = f.Close() }()
	lines, offset, _, _ := seedFile(f, maxLines)
	return lines, offset
}

// seedFile reads the retained descriptor, leaving a partial trailing line for
// the live reader. Cloning each retained line releases the large read buffer.
func seedFile(f *os.File, maxLines int) ([]string, int64, bool, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	size := fi.Size()
	if maxLines <= 0 { // no history, including an already-started trailing record
		var last [1]byte
		_, _ = f.ReadAt(last[:], size-1)
		return nil, size, size > 0 && last[0] != '\n', nil
	}
	if maxLines > 100000 {
		return nil, size, false, fmt.Errorf("seed_lines exceeds 100000")
	}
	var head [2]byte
	_, _ = f.ReadAt(head[:], 0)
	if head[0] == 0x1f && head[1] == 0x8b || strings.HasSuffix(f.Name(), ".gz") {
		lines, err := seedGzipChecked(f, maxLines, 64<<20)
		return lines, size, false, err
	}
	const chunk = 64 * 1024
	var buf []byte
	off := size
	for off > 0 && size-off < 4*chunk {
		step := min(off, chunk)
		off -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
			return nil, size, false, err
		}
		buf = append(b, buf...)
		if bytes.Count(buf, []byte{'\n'}) > maxLines {
			break
		}
	}
	resume := size
	end := len(buf)
	if end > 0 && buf[end-1] != '\n' {
		pos := bytes.LastIndexByte(buf, '\n')
		if pos < 0 {
			if off == 0 {
				return nil, 0, false, nil
			}
			return nil, size, true, fmt.Errorf("%s: startup partial line exceeds seed byte budget", f.Name())
		}
		end = pos + 1
		resume = off + int64(end)
	}
	text := string(buf[:end])
	lines := strings.Split(text, "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for i := range lines {
		lines[i] = strings.Clone(lines[i])
	}
	if off > 0 && len(lines) < maxLines {
		err = fmt.Errorf("%s: seed history limited to 256 KiB", f.Name())
	}
	return lines, resume, false, err
}

func seedGzipChecked(f *os.File, maxLines int, limit int64) ([]string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	lr := &io.LimitedReader{R: zr, N: limit + 1}
	sc := bufio.NewScanner(lr)
	sc.Buffer(make([]byte, 64*1024), filetail.MaxLine)
	ring := make([]string, maxLines)
	n, head := 0, 0
	for sc.Scan() {
		if lr.N == 0 {
			return nil, fmt.Errorf("%s: gzip history exceeds %d decompressed bytes", f.Name(), limit)
		}
		line := sc.Text()
		if n < maxLines {
			ring[n] = line
			n++
		} else {
			ring[head] = line
			head = (head + 1) % maxLines
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if lr.N == 0 {
		return nil, fmt.Errorf("%s: gzip history exceeds %d decompressed bytes", f.Name(), limit)
	}
	if n < maxLines {
		return ring[:n], nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = ring[(head+i)%n]
	}
	return out, nil
}

// Tailer fans out seed+tail goroutines for discovered sources and rescans
// the globs periodically for new log files. Lines arrive on Ch until Stop.
type Tailer struct {
	Ch          chan RawLine
	globs       []string
	seedN       int
	vhostMap    map[string]string
	rescanEvery time.Duration // test override; default 60s

	stopCh      chan struct{}
	mu          sync.Mutex
	running     map[string]*Source
	srcOrder    []*Source
	wg          sync.WaitGroup
	stopped     bool
	stopOnce    sync.Once
	seedSlots   chan struct{}
	diagnostics map[string]string
}

// Option customizes a Tailer at Start time.
type Option func(*Tailer)

// WithRescanEvery overrides the new-file rescan interval (default 60s).
func WithRescanEvery(d time.Duration) Option {
	return func(t *Tailer) { t.rescanEvery = d }
}

// Start begins tailing the given initial sources and keeps rescanning the
// globs (skipped when empty) periodically for new files. Sources from
// detection are exact; rescan-discovered files get vhosts from vhostMap or
// the filename.
func Start(initial []Source, globs []string, seedN int, vhostMap map[string]string, opts ...Option) *Tailer {
	t := &Tailer{
		Ch:          make(chan RawLine, 4096),
		globs:       globs,
		seedN:       seedN,
		vhostMap:    vhostMap,
		stopCh:      make(chan struct{}),
		running:     map[string]*Source{},
		seedSlots:   make(chan struct{}, 4),
		diagnostics: map[string]string{},
	}
	for _, o := range opts {
		o(t)
	}
	for i := range initial {
		t.startSource(&initial[i])
	}
	if len(globs) > 0 {
		// Capture the interval before the goroutine starts so callers
		// cannot race it via later mutation.
		every := t.rescanEvery
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			for {
				d := every
				if d <= 0 {
					d = 60 * time.Second
				}
				select {
				case <-t.stopCh:
					return
				case <-time.After(d):
					t.rescan()
				}
			}
		}()
	}
	return t
}

// Sources returns the currently tracked sources.
func (t *Tailer) Sources() []Source {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Source, 0, len(t.srcOrder))
	for _, s := range t.srcOrder {
		out = append(out, *s)
	}
	return out
}

// Stop terminates all tailers and closes Ch.
func (t *Tailer) Stop() {
	t.stopOnce.Do(func() {
		t.mu.Lock()
		t.stopped = true
		close(t.stopCh)
		t.mu.Unlock()
		t.wg.Wait()
		close(t.Ch)
	})
}

func (t *Tailer) rescan() {
	for _, src := range Discover(t.globs, t.vhostMap) {
		// Rotated files surfacing mid-run are old content of already
		// followed logs (rotation in progress): replaying them would
		// double-count. Rotated history is a startup-seed concept only.
		if src.Replay {
			continue
		}
		t.startSource(&src)
	}
}

func (t *Tailer) startSource(src *Source) {
	t.mu.Lock()
	if _, dup := t.running[src.Path]; dup || t.stopped {
		t.mu.Unlock()
		return
	}
	s := *src
	t.running[s.Path] = &s
	t.srcOrder = append(t.srcOrder, &s)
	t.wg.Add(1)
	t.mu.Unlock()
	go t.runSource(&s)
}

// forgetSource drops an exited tailer from the running set so the rescan
// can restart it (self-healing after e.g. a failed reopen). The restart
// re-seeds, which may re-deliver a few of the most recent lines — the rare
// cost of never getting stuck.
func (t *Tailer) forgetSource(src *Source) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cur, ok := t.running[src.Path]; ok && cur == src {
		delete(t.running, src.Path)
	}
	for i, s := range t.srcOrder {
		if s == src {
			t.srcOrder = append(t.srcOrder[:i], t.srcOrder[i+1:]...)
			break
		}
	}
}

// Errors returns bounded per-source diagnostics without mutable source fields.
func (t *Tailer) Errors() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.diagnostics))
	for k, v := range t.diagnostics {
		out[k] = v
	}
	return out
}
func (t *Tailer) report(path string, err error) {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.diagnostics) < 512 || t.diagnostics[path] != "" {
		t.diagnostics[path] = err.Error()
	}
}

// runSource retains the seed descriptor through live following, so rotation
// cannot turn an old offset into a seek on a new inode.
func (t *Tailer) runSource(src *Source) {
	defer t.wg.Done()
	defer t.forgetSource(src)
	select {
	case t.seedSlots <- struct{}{}:
	case <-t.stopCh:
		return
	}
	f, err := filetail.Open(src.Path)
	var seed []string
	var offset int64
	var skip bool
	if err == nil {
		seed, offset, skip, err = seedFile(f, t.seedN)
	}
	t.report(src.Path, err)
	reader := filetail.New(src.Path, f, offset, skip)
	defer func() { _ = reader.Close() }()
	send := func(lines []string, seeded bool) bool {
		for _, l := range lines {
			select {
			case t.Ch <- RawLine{Text: l, Source: src, Seeded: seeded}:
			case <-t.stopCh:
				return false
			}
		}
		return true
	}
	seedSent := send(seed, true)
	<-t.seedSlots
	if !seedSent || src.Replay {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopCh:
			return
		default:
		}
		lines, more, err := reader.Read(256 * 1024)
		t.report(src.Path, err)
		if !send(lines, false) {
			return
		}
		if more {
			continue
		}
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
		}
	}
}
