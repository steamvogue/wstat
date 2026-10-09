// Package logsrc discovers access logs, attributes vhosts from filenames,
// and tails them rotation-safely (Apache combined-family formats only).
package logsrc

import (
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nxadm/tail"
)

// Source is one tailed access log file.
type Source struct {
	Path   string
	Vhost  string // fallback vhost derived from the filename
	Replay bool   // rotated/compressed history: replay once, never tail
}

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

// Discover finds readable, non-empty access-log files matching the globs
// (DefaultGlobs when nil). Error logs are skipped. Files that are not plain
// "*.log" (e.g. ".log.1", ".log.2.gz") are classified as Replay. Sorted by
// path.
func Discover(globs []string) []Source {
	if len(globs) == 0 {
		globs = DefaultGlobs
	}
	seen := map[string]bool{}
	var out []Source
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil || len(matches) == 0 {
			continue
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
			if err != nil || fi.IsDir() || fi.Size() == 0 {
				continue
			}
			live := strings.HasSuffix(base, ".log")
			out = append(out, Source{
				Path:   p,
				Vhost:  vhostFromFilename(rotationSuffix.ReplaceAllString(base, "")),
				Replay: !live,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func vhostFromFilename(base string) string {
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
func seedLines(path string, maxLines int) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return nil, 0
	}
	size := fi.Size()
	if maxLines <= 0 {
		return nil, size
	}
	var head [2]byte
	if _, err := f.ReadAt(head[:], 0); err == nil && head[0] == 0x1f && head[1] == 0x8b {
		return seedGzip(f, maxLines), size
	}
	const chunk = 64 * 1024
	var buf []byte
	off := size
	for off > 0 && size-off < 4*chunk {
		step := off
		if step > chunk {
			step = chunk
		}
		off -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
			return nil, size
		}
		buf = append(b, buf...)
	}
	lines := strings.Split(string(buf), "\n")
	// Drop the first line: partial unless we read from offset 0.
	if off > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	// Drop trailing empty piece from the final newline.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	// If the buffer ends mid-line, the tailer re-delivers that line fully;
	// drop the partial copy so it is not double-counted.
	if len(buf) > 0 && buf[len(buf)-1] != '\n' && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, size
}

// seedGzip decompresses (bounded) and returns the last maxLines lines.
func seedGzip(f *os.File, maxLines int) []string {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil
	}
	defer func() { _ = zr.Close() }()
	ring := make([]string, maxLines)
	n, head := 0, 0
	var total int
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if n < maxLines {
			ring[n] = line
			n++
		} else {
			ring[head] = line
			head = (head + 1) % maxLines
		}
		if total += len(line); total > 64<<20 {
			break // decompression bomb guard
		}
	}
	if n < maxLines {
		return ring[:n]
	}
	out := make([]string, maxLines)
	for i := 0; i < maxLines; i++ {
		out[i] = ring[(head+i)%maxLines]
	}
	return out
}

// Tailer fans out seed+tail goroutines for discovered sources and rescans
// the globs periodically for new log files. Lines arrive on Ch until Stop.
type Tailer struct {
	Ch    chan RawLine
	globs []string
	seedN int

	stopCh   chan struct{}
	mu       sync.Mutex
	running  map[string]*Source
	srcOrder []*Source
	wg       sync.WaitGroup
}

// Start discovers sources once (synchronously) and keeps scanning every
// minute for new files.
func Start(globs []string, seedN int) *Tailer {
	t := &Tailer{
		Ch:      make(chan RawLine, 4096),
		globs:   globs,
		seedN:   seedN,
		stopCh:  make(chan struct{}),
		running: map[string]*Source{},
	}
	t.rescan()
	go func() {
		tick := time.NewTicker(60 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-t.stopCh:
				return
			case <-tick.C:
				t.rescan()
			}
		}
	}()
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
	close(t.stopCh)
	t.wg.Wait()
	close(t.Ch)
}

func (t *Tailer) rescan() {
	for _, src := range Discover(t.globs) {
		t.mu.Lock()
		if _, dup := t.running[src.Path]; dup {
			t.mu.Unlock()
			continue
		}
		s := src
		t.running[s.Path] = &s
		t.srcOrder = append(t.srcOrder, &s)
		t.mu.Unlock()
		t.wg.Add(1)
		go t.runSource(&s)
	}
}

// runSource seeds the file's tail and then follows it with tail -F
// semantics (rename/recreate and truncate rotation safe). Replay sources
// (rotated/gz history) emit their seed and stop: they never tail. The seed
// read and the tail handoff share one byte offset so every line is counted
// exactly once.
func (t *Tailer) runSource(src *Source) {
	defer t.wg.Done()
	seed, offset := seedLines(src.Path, t.seedN)
	for _, l := range seed {
		select {
		case t.Ch <- RawLine{Text: l, Source: src, Seeded: true}:
		case <-t.stopCh:
			return
		}
	}
	if src.Replay {
		return
	}
	cfg := tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: false,
		// Poll instead of inotify: polling has no missed-event race for
		// appends that land between open and watch registration, at a
		// negligible cost (one stat per file every 250ms).
		Poll:     true,
		Logger:   tail.DiscardingLogger,
		Location: &tail.SeekInfo{Offset: offset, Whence: io.SeekStart},
	}
	// If the file was replaced (rotated) between seed and open, the offset
	// belongs to the old inode: read the new file from 0.
	if fi, err := os.Stat(src.Path); err == nil && fi.Size() < offset {
		cfg.Location = &tail.SeekInfo{Offset: 0, Whence: io.SeekStart}
	}
	tf, err := tail.TailFile(src.Path, cfg)
	if err != nil {
		return
	}
	defer func() { _ = tf.Stop() }()
	for {
		select {
		case <-t.stopCh:
			return
		case line, ok := <-tf.Lines:
			if !ok {
				return
			}
			if line.Err != nil {
				continue
			}
			select {
			case t.Ch <- RawLine{Text: line.Text, Source: src}:
			case <-t.stopCh:
				return
			}
		}
	}
}
