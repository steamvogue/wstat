// Package store aggregates parsed access-log records into bounded live
// state: per-host/URL/client counters with EWMA rates, a stream ring, and
// filterable snapshots for the UI.
package store

import (
	"sort"
	"sync"
	"time"

	"wstat/internal/parser"
)

const (
	streamCap  = 500
	maxKeys    = 20000 // hard cap on url/client rows (insert-time, bounded memory)
	staleAfter = 2 * time.Minute
	ewmaAlpha  = 0.4
	maxUALen   = 80
)

// StatusMask filters status classes. Zero means "all". Bit (1<<i) covers
// class (i+2)xx.
type StatusMask uint8

const (
	Mask2xx StatusMask = 1 << 0
	Mask3xx StatusMask = 1 << 1
	Mask4xx StatusMask = 1 << 2
	Mask5xx StatusMask = 1 << 3
	MaskErr            = Mask4xx | Mask5xx
	MaskOK             = Mask2xx | Mask3xx
)

func (m StatusMask) Allows(status int) bool {
	if m == 0 {
		return true
	}
	idx, ok := classIdx(status)
	if !ok {
		return false
	}
	return m&(1<<idx) != 0
}

func classIdx(status int) (int, bool) {
	c := status / 100
	if c < 2 || c > 5 {
		return 0, false
	}
	return c - 2, true
}

type agg struct {
	hits  int64
	bytes int64
	errs  int64 // 4xx+5xx
	class [4]int64
	cur   int64 // hits since last flush
	rate  float64
	last  time.Time // wall clock of last hit
}

func (a *agg) add(r parser.Record, now time.Time, live bool) {
	a.hits++
	a.bytes += r.Bytes
	if live {
		a.cur++
	}
	a.last = now
	if idx, ok := classIdx(r.Status); ok {
		a.class[idx]++
	}
	if r.Status >= 400 {
		a.errs++
	}
}

// filtered returns hit count under the active status mask.
func (a *agg) filtered(mask StatusMask) int64 {
	if mask == 0 {
		return a.hits
	}
	n := int64(0)
	for i := 0; i < 4; i++ {
		if mask&(1<<i) != 0 {
			n += a.class[i]
		}
	}
	return n
}

type urlAgg struct {
	agg
	vhost  string
	method string
	path   string
}

type clientAgg struct {
	agg
	ua     string
	bot    bool
	vhosts map[string]int64
}

type totals struct {
	reqs, bytes int64
	class       [4]int64
	bots        int64
	curReqs     int64
	curBytes    int64
	rate        float64
	byteRate    float64
}

// Store is safe for concurrent use.
type Store struct {
	mu             sync.Mutex
	hosts          map[string]*agg
	urls           map[string]*urlAgg
	clients        map[string]*clientAgg
	stream         []parser.Record
	streamLen      int
	streamHead     int
	tot            totals
	bad            int64
	urlOverflow    int64 // unique urls beyond maxKeys (not tracked)
	clientOverflow int64 // unique clients beyond maxKeys (not tracked)
	lastFlush      time.Time
}

func New() *Store {
	return &Store{
		hosts:   map[string]*agg{},
		urls:    map[string]*urlAgg{},
		clients: map[string]*clientAgg{},
	}
}

// Add ingests one parsed live record (feeds rate counters).
func (s *Store) Add(r parser.Record) { s.add(r, true) }

// AddSeed ingests one record replayed from history at startup: it counts in
// totals and tables but does not feed live rate counters, so a large seed
// burst does not fake a traffic spike.
func (s *Store) AddSeed(r parser.Record) { s.add(r, false) }

func (s *Store) add(r parser.Record, live bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pushStream(r)
	s.tot.reqs++
	s.tot.bytes += r.Bytes
	if live {
		s.tot.curReqs++
		s.tot.curBytes += r.Bytes
	}
	if idx, ok := classIdx(r.Status); ok {
		s.tot.class[idx]++
	}
	if r.Bot {
		s.tot.bots++
	}

	ha := s.hosts[r.Vhost]
	if ha == nil {
		ha = &agg{}
		s.hosts[r.Vhost] = ha
	}
	ha.add(r, now, live)

	key := r.Vhost + "\x00" + r.Method + "\x00" + r.Path
	ua := s.urls[key]
	if ua == nil {
		if len(s.urls) >= maxKeys {
			s.urlOverflow++
		} else {
			ua = &urlAgg{vhost: r.Vhost, method: r.Method, path: r.Path}
			s.urls[key] = ua
		}
	}
	if ua != nil {
		ua.add(r, now, live)
	}

	ca := s.clients[r.IP]
	if ca == nil {
		if len(s.clients) >= maxKeys {
			s.clientOverflow++
		} else {
			ca = &clientAgg{vhosts: map[string]int64{}}
			s.clients[r.IP] = ca
		}
	}
	if ca != nil {
		if ca.ua == "" && r.UA != "" && r.UA != "-" {
			ca.ua = truncate(r.UA, maxUALen)
		}
		if r.Bot {
			ca.bot = true
		}
		ca.add(r, now, live)
		ca.vhosts[r.Vhost]++
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// AddBad counts an unparseable line.
func (s *Store) AddBad() {
	s.mu.Lock()
	s.bad++
	s.mu.Unlock()
}

func (s *Store) pushStream(r parser.Record) {
	if s.streamLen < streamCap {
		s.stream = append(s.stream, r)
		s.streamLen++
		if s.streamLen == streamCap {
			s.streamHead = 0
		}
		return
	}
	s.stream[s.streamHead] = r
	s.streamHead = (s.streamHead + 1) % streamCap
}

func (s *Store) maybeFlushLocked(now time.Time) {
	if !s.lastFlush.IsZero() && now.Sub(s.lastFlush) < 400*time.Millisecond {
		return
	}
	dt := 1.0
	if !s.lastFlush.IsZero() {
		if d := now.Sub(s.lastFlush).Seconds(); d > 0 {
			dt = d
		}
	}
	upd := func(a *agg) {
		a.rate = a.rate*(1-ewmaAlpha) + (float64(a.cur)/dt)*ewmaAlpha
		a.cur = 0
	}
	for _, a := range s.hosts {
		upd(a)
	}
	for _, u := range s.urls {
		upd(&u.agg)
	}
	for _, c := range s.clients {
		upd(&c.agg)
	}
	s.tot.rate = s.tot.rate*(1-ewmaAlpha) + (float64(s.tot.curReqs)/dt)*ewmaAlpha
	s.tot.byteRate = s.tot.byteRate*(1-ewmaAlpha) + (float64(s.tot.curBytes)/dt)*ewmaAlpha
	s.tot.curReqs, s.tot.curBytes = 0, 0
	s.lastFlush = now
	s.evictLocked(now)
}

// evictLocked drops stale entries when maps exceed their budget.
func (s *Store) evictLocked(now time.Time) {
	if len(s.urls) > maxKeys {
		for k, u := range s.urls {
			if now.Sub(u.last) > staleAfter {
				delete(s.urls, k)
			}
		}
	}
	if len(s.clients) > maxKeys {
		for k, c := range s.clients {
			if now.Sub(c.last) > staleAfter {
				delete(s.clients, k)
			}
		}
	}
}

// Row is one displayable table row.
type Row struct {
	Key    string // vhost or client IP
	Vhost  string
	Method string
	Path   string
	UA     string
	Bot    bool
	Rate   float64
	Hits   int64
	Bytes  int64
	Errs   int64
	Last   time.Time
}

// Totals is the global header summary.
type Totals struct {
	Reqs, Bytes int64
	Class       [4]int64
	Bots        int64
	Rate        float64
	ByteRate    float64
}

// Snapshot returns a display-ready copy of the state. Hosts is the active
// host filter (nil/empty = all); it scopes URLs, clients and stream but not
// the hosts panel itself. Mask scopes status classes across all panels.
func (s *Store) Snapshot(hosts map[string]bool, mask StatusMask, topN int) (hostsRows []Row, urlRows []Row, clientRows []Row, stream []parser.Record, tot Totals, bad int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.maybeFlushLocked(now)

	for k, a := range s.hosts {
		hostsRows = append(hostsRows, Row{Key: k, Rate: a.rate, Hits: a.filtered(mask), Bytes: a.bytes, Errs: a.errs, Last: a.last})
	}
	sortRows(hostsRows, mask)
	if len(hostsRows) > topN {
		hostsRows = hostsRows[:topN]
	}

	for _, u := range s.urls {
		if len(hosts) > 0 && !hosts[u.vhost] {
			continue
		}
		urlRows = append(urlRows, Row{
			Key: u.vhost + " " + u.method + " " + u.path, Vhost: u.vhost,
			Method: u.method, Path: u.path, Rate: u.rate,
			Hits: u.filtered(mask), Bytes: u.bytes, Errs: u.errs, Last: u.last,
		})
	}
	sortRows(urlRows, mask)
	if len(urlRows) > topN {
		urlRows = urlRows[:topN]
	}

	for k, c := range s.clients {
		hits := int64(0)
		if len(hosts) > 0 {
			// Host filter is exact via per-vhost counters; combining it with
			// a status mask is approximate (intersection not tracked).
			for h, n := range c.vhosts {
				if hosts[h] {
					hits += n
				}
			}
			if hits == 0 {
				continue
			}
		} else {
			hits = c.filtered(mask)
		}
		clientRows = append(clientRows, Row{
			Key: k, Rate: c.rate, Hits: hits, Bytes: c.bytes, Errs: c.errs,
			UA: c.ua, Bot: c.bot, Last: c.last,
		})
	}
	sortRows(clientRows, mask)
	if len(clientRows) > topN {
		clientRows = clientRows[:topN]
	}

	stream = s.snapshotStreamLocked(hosts, mask, 100)
	tot = Totals{Reqs: s.tot.reqs, Bytes: s.tot.bytes, Class: s.tot.class, Bots: s.tot.bots, Rate: s.tot.rate, ByteRate: s.tot.byteRate}
	return hostsRows, urlRows, clientRows, stream, tot, s.bad
}

func sortRows(rows []Row, mask StatusMask) {
	byRate := mask == 0
	sort.Slice(rows, func(i, j int) bool {
		if byRate {
			if rows[i].Rate != rows[j].Rate {
				return rows[i].Rate > rows[j].Rate
			}
			return rows[i].Hits > rows[j].Hits
		}
		if rows[i].Hits != rows[j].Hits {
			return rows[i].Hits > rows[j].Hits
		}
		return rows[i].Rate > rows[j].Rate
	})
}

func (s *Store) snapshotStreamLocked(hosts map[string]bool, mask StatusMask, max int) []parser.Record {
	out := make([]parser.Record, 0, 64)
	for i := 1; i <= s.streamLen && len(out) < max; i++ {
		var r parser.Record
		if s.streamLen < streamCap {
			r = s.stream[s.streamLen-i]
		} else {
			r = s.stream[(s.streamHead-i+streamCap)%streamCap]
		}
		if len(hosts) > 0 && !hosts[r.Vhost] {
			continue
		}
		if !mask.Allows(r.Status) {
			continue
		}
		out = append(out, r)
	}
	// Collected newest-first; flip to chronological.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
