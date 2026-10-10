// Package store aggregates parsed access-log records into bounded live
// state: per-host/URL/client counters with EWMA rates, a stream ring, and
// filterable snapshots for the UI.
package store

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steamvogue/wstat/internal/parser"
)

const (
	streamCap       = 500
	maxKeys         = 20000 // hard cap on url/client rows (insert-time, bounded memory)
	staleAfter      = 2 * time.Minute
	ewmaAlpha       = 0.4
	maxUALen        = 80
	maxHosts        = 2048
	maxAssociations = 40000
	maxClientHosts  = 64
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

// SortKey selects the primary sort of a panel's rows.
type SortKey uint8

const (
	SortRate SortKey = iota
	SortHits
	SortErrs
	SortBytes
)

func (k SortKey) String() string {
	switch k {
	case SortHits:
		return "hits"
	case SortErrs:
		return "errs"
	case SortBytes:
		return "bytes"
	default:
		return "rate"
	}
}

// Filters scopes supported dimensions exactly. Hosts ignore host selection;
// URLs apply host+method, clients apply host. All tables apply the joint
// status/bot/static predicates to every displayed metric. Client/path selection
// scopes the stream only and marks its own table; method does not scope hosts
// or clients. Global totals are unfiltered. Detail eviction limits history.
type Filters struct {
	Hosts   map[string]bool // selected vhosts
	Clients map[string]bool // selected client IPs
	Paths   map[string]bool // selected URL paths
	Mask    StatusMask
	Method  string // "" = all
	Bots    int8   // 0 all, +1 bots only, -1 humans only
	Static  int8   // 0 all, -1 hide static assets
}

func (f Filters) Any() bool {
	return len(f.Hosts) > 0 || len(f.Clients) > 0 || len(f.Paths) > 0 ||
		f.Mask != 0 || f.Method != "" || f.Bots != 0 || f.Static != 0
}

type metric struct {
	hits, bytes, errs, cur int64
	rate                   float64
	last                   time.Time
	latSum, latCount       int64
}

func (m *metric) add(r parser.Record, now time.Time, live bool) {
	m.hits++
	m.bytes += r.Bytes
	if r.Status >= 400 && r.Status < 600 {
		m.errs++
	}
	if live {
		m.cur++
	}
	m.last = now
	if r.LatencyUs > 0 {
		m.latSum += r.LatencyUs
		m.latCount++
	}
}
func (m *metric) flush(dt float64) {
	m.rate = m.rate*(1-ewmaAlpha) + float64(m.cur)/dt*ewmaAlpha
	m.cur = 0
}
func (m metric) latAvg() int64 {
	if m.latCount == 0 {
		return 0
	}
	return m.latSum / m.latCount
}
func (m *metric) merge(v metric) {
	m.hits += v.hits
	m.bytes += v.bytes
	m.errs += v.errs
	m.cur += v.cur
	m.rate += v.rate
	m.latSum += v.latSum
	m.latCount += v.latCount
	if v.last.After(m.last) {
		m.last = v.last
	}
}

type bucket struct {
	metric
	tag uint8
}
type agg struct {
	metric
	joint      []bucket // sparse: at most 5 status classes * 2 bot * 2 static = 20
	key        string
	prev, next *agg // intrusive LRU: touching a row allocates nothing
}

func recordTag(r parser.Record) uint8 {
	var tag uint8
	if i, ok := classIdx(r.Status); ok {
		tag = uint8(i + 1)
	}
	if r.Bot {
		tag |= 8
	}
	if r.Static {
		tag |= 16
	}
	return tag
}
func (a *agg) add(r parser.Record, now time.Time, live bool) {
	a.metric.add(r, now, live)
	tag := recordTag(r)
	for i := range a.joint {
		if a.joint[i].tag == tag {
			a.joint[i].add(r, now, live)
			return
		}
	}
	a.joint = append(a.joint, bucket{tag: tag})
	a.joint[len(a.joint)-1].add(r, now, live)
}
func (a *agg) filteredMetric(f Filters) metric {
	if f.Mask == 0 && f.Bots == 0 && f.Static == 0 {
		return a.metric
	}
	var out metric
	for _, b := range a.joint {
		c := b.tag & 7
		if f.Mask != 0 && (c == 0 || f.Mask&(1<<(c-1)) == 0) {
			continue
		}
		bot := b.tag&8 != 0
		static := b.tag&16 != 0
		if f.Bots == 1 && !bot || f.Bots == -1 && bot || f.Static == -1 && static {
			continue
		}
		out.merge(b.metric)
	}
	return out
}
func (a *agg) flush(dt float64) {
	a.metric.flush(dt)
	for i := range a.joint {
		a.joint[i].flush(dt)
	}
}

type lru struct{ head, tail *agg }

func (q *lru) remove(a *agg) {
	if a.prev != nil {
		a.prev.next = a.next
	} else {
		q.head = a.next
	}
	if a.next != nil {
		a.next.prev = a.prev
	} else {
		q.tail = a.prev
	}
	a.prev, a.next = nil, nil
}
func (q *lru) touch(a *agg) {
	if q.tail == a {
		return
	}
	if a.prev != nil || a.next != nil || q.head == a {
		q.remove(a)
	}
	a.prev = q.tail
	if q.tail != nil {
		q.tail.next = a
	} else {
		q.head = a
	}
	q.tail = a
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
	vhosts map[string]*agg
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
	mu                                  sync.Mutex
	hosts                               map[string]*agg
	urls                                map[string]*urlAgg
	clients                             map[string]*clientAgg
	stream                              []parser.Record
	streamLen                           int
	streamHead                          int
	streamSequence                      uint64
	tot                                 totals
	bad                                 int64
	urlOverflow                         int64
	clientOverflow                      int64
	lastFlush                           time.Time
	hostLRU, urlLRU, clientLRU, pairLRU lru
	pairs                               map[string]*association
	hostOverflow, pairOverflow          int64
	now                                 func() time.Time
	rateActive                          map[*agg]struct{}
	generation                          uint64
	cache                               *snapshotCache
}

type association struct {
	a      *agg
	client *clientAgg
	host   string
}

func New() *Store {
	return &Store{
		hosts:   map[string]*agg{},
		urls:    map[string]*urlAgg{},
		clients: map[string]*clientAgg{},
		pairs:   map[string]*association{}, now: time.Now, rateActive: map[*agg]struct{}{},
	}
}

// Add ingests one parsed live record (feeds rate counters).
func (s *Store) Add(r parser.Record) { s.add(r, true) }

// AddSeed ingests one record replayed from history at startup: it counts in
// totals and tables but does not feed live rate counters, so a large seed
// burst does not fake a traffic spike.
func (s *Store) AddSeed(r parser.Record) { s.add(r, false) }

func (s *Store) add(r parser.Record, live bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.generation++
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
		s.admitHost(now)
		ha = &agg{key: r.Vhost}
		s.hosts[r.Vhost] = ha
	}
	s.addMetric(ha, r, now, live)
	s.hostLRU.touch(ha)

	key := r.Vhost + "\x00" + r.Method + "\x00" + r.Path
	ua := s.urls[key]
	if ua == nil {
		s.admitURL(now)
		ua = &urlAgg{agg: agg{key: key}, vhost: r.Vhost, method: r.Method, path: r.Path}
		s.urls[key] = ua
	}
	s.addMetric(&ua.agg, r, now, live)
	s.urlLRU.touch(&ua.agg)

	ca := s.clients[r.IP]
	if ca == nil {
		s.admitClient(now)
		ca = &clientAgg{agg: agg{key: r.IP}, vhosts: map[string]*agg{}}
		s.clients[r.IP] = ca
	}
	if ca.ua == "" && r.UA != "" && r.UA != "-" {
		ca.ua = truncate(r.UA, maxUALen)
	}
	s.addMetric(&ca.agg, r, now, live)
	s.clientLRU.touch(&ca.agg)
	ca.bot = ca.bot || r.Bot
	pair := ca.vhosts[r.Vhost]
	if pair == nil {
		if len(ca.vhosts) >= maxClientHosts {
			var oldest *agg
			for _, v := range ca.vhosts {
				if oldest == nil || v.last.Before(oldest.last) || v.last.Equal(oldest.last) && v.key < oldest.key {
					oldest = v
				}
			}
			s.dropPair(oldest)
		}
		for len(s.pairs) >= maxAssociations {
			s.dropPair(s.pairLRU.head)
		}
		pair = &agg{key: r.IP + "\x00" + r.Vhost}
		ca.vhosts[r.Vhost] = pair
		s.pairs[pair.key] = &association{a: pair, client: ca, host: r.Vhost}
	}
	s.addMetric(pair, r, now, live)
	s.pairLRU.touch(pair)

}

func (s *Store) addMetric(a *agg, r parser.Record, now time.Time, live bool) {
	a.add(r, now, live)
	if live {
		s.rateActive[a] = struct{}{}
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return strings.Clone(s[:n])
	}
	return strings.Clone(s)
}

// AddBad counts an unparseable line.
func (s *Store) AddBad() {
	s.mu.Lock()
	s.bad++
	s.mu.Unlock()
}

func (s *Store) pushStream(r parser.Record) {
	s.streamSequence++
	r.StreamID = s.streamSequence
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
	// Seed-only rows never have live rate work. Track active aggregates until
	// their EWMA becomes negligible instead of walking every retained bucket.
	if len(s.rateActive) > 0 {
		s.generation++
	}
	for a := range s.rateActive {
		a.flush(dt)
		if a.rate < 1e-9 {
			a.rate = 0
			for i := range a.joint {
				a.joint[i].rate = 0
			}
			delete(s.rateActive, a)
		}
	}
	s.tot.rate = s.tot.rate*(1-ewmaAlpha) + (float64(s.tot.curReqs)/dt)*ewmaAlpha
	s.tot.byteRate = s.tot.byteRate*(1-ewmaAlpha) + (float64(s.tot.curBytes)/dt)*ewmaAlpha
	s.tot.curReqs, s.tot.curBytes = 0, 0
	s.lastFlush = now
}

// Admission expires stale LRU heads at capacity, then replaces the least
// recently used row. Lifetime totals never depend on retained detail rows.
func (s *Store) admitHost(now time.Time) {
	if len(s.hosts) < maxHosts {
		return
	}
	for s.hostLRU.head != nil {
		a := s.hostLRU.head
		s.hostLRU.remove(a)
		delete(s.hosts, a.key)
		delete(s.rateActive, a)
		s.hostOverflow++
		if len(s.hosts) < maxHosts && (s.hostLRU.head == nil || now.Sub(s.hostLRU.head.last) <= staleAfter) {
			break
		}
	}
}
func (s *Store) admitURL(now time.Time) {
	if len(s.urls) < maxKeys {
		return
	}
	for s.urlLRU.head != nil {
		a := s.urlLRU.head
		s.urlLRU.remove(a)
		delete(s.urls, a.key)
		delete(s.rateActive, a)
		s.urlOverflow++
		if len(s.urls) < maxKeys && (s.urlLRU.head == nil || now.Sub(s.urlLRU.head.last) <= staleAfter) {
			break
		}
	}
}
func (s *Store) admitClient(now time.Time) {
	if len(s.clients) < maxKeys {
		return
	}
	for s.clientLRU.head != nil {
		a := s.clientLRU.head
		c := s.clients[a.key]
		for _, p := range c.vhosts {
			s.dropPair(p)
		}
		s.clientLRU.remove(a)
		delete(s.clients, a.key)
		delete(s.rateActive, a)
		s.clientOverflow++
		if len(s.clients) < maxKeys && (s.clientLRU.head == nil || now.Sub(s.clientLRU.head.last) <= staleAfter) {
			break
		}
	}
}
func (s *Store) dropPair(a *agg) {
	p := s.pairs[a.key]
	delete(p.client.vhosts, p.host)
	delete(s.pairs, a.key)
	delete(s.rateActive, a)
	s.pairLRU.remove(a)
	s.pairOverflow++
}

// Row is one displayable table row.
type Row struct {
	Key       string // vhost or client IP
	Vhost     string
	Method    string
	Path      string
	UA        string
	Bot       bool
	Rate      float64
	Hits      int64 // hits under the active filters
	Bytes     int64
	Errs      int64
	LatencyUs int64 // mean request duration when a latency source exists
	Last      time.Time
}

// Totals is the global header summary (never filtered).
type Totals struct {
	Reqs, Bytes                                                int64
	Class                                                      [4]int64
	Bots                                                       int64
	Rate                                                       float64
	ByteRate                                                   float64
	HostEvicted, URLEvicted, ClientEvicted, AssociationEvicted int64
	TrackedHosts                                               int
}

// Snapshot returns immutable display-ready state under the given
// filters. sorts holds the sort keys for hosts, urls and clients.
func (s *Store) Snapshot(f Filters, sorts [3]SortKey, topN int) (hostsRows []Row, urlRows []Row, clientRows []Row, stream []parser.Record, tot Totals, bad int64) {
	if topN < 0 {
		topN = 0
	}
	key := cacheKey(f, sorts, topN)
	s.mu.Lock()
	now := s.now()
	s.maybeFlushLocked(now)
	if c := s.cache; c != nil && c.generation == s.generation && c.key == key {
		tot, bad = s.totalsLocked(), s.bad
		s.mu.Unlock()
		return c.hosts, c.urls, c.clients, c.stream, tot, bad
	}
	generation := s.generation
	hostsRows = make([]Row, 0, min(topN, len(s.hosts)))
	urlRows = make([]Row, 0, min(topN, len(s.urls)))
	clientRows = make([]Row, 0, min(topN, len(s.clients)))

	for k, a := range s.hosts {
		m := a.filteredMetric(f)
		if m.hits == 0 {
			continue
		}
		hostsRows = offerRow(hostsRows, topN, sorts[0], Row{
			Key: k, Rate: m.rate, Hits: m.hits,
			Bytes: m.bytes, Errs: m.errs, LatencyUs: m.latAvg(), Last: m.last,
		})
	}

	for key, u := range s.urls {
		// Non-self filters that are exact for this dimension.
		if len(f.Hosts) > 0 && !f.Hosts[u.vhost] {
			continue
		}
		if f.Method != "" && u.method != f.Method {
			continue
		}
		m := u.filteredMetric(f)
		if m.hits == 0 {
			continue
		}

		urlRows = offerRow(urlRows, topN, sorts[1], Row{
			Key:   key,
			Vhost: u.vhost, Method: u.method, Path: u.path,
			Rate: m.rate, Hits: m.hits, Bytes: m.bytes, Errs: m.errs,
			LatencyUs: m.latAvg(), Last: m.last,
		})
	}

	for k, c := range s.clients {
		var m metric
		if len(f.Hosts) > 0 {
			for h, a := range c.vhosts {
				if f.Hosts[h] {
					m.merge(a.filteredMetric(f))
				}
			}
		} else {
			m = c.filteredMetric(f)
		}
		if m.hits == 0 {
			continue
		}
		clientRows = offerRow(clientRows, topN, sorts[2], Row{Key: k, Rate: m.rate, Hits: m.hits, Bytes: m.bytes, Errs: m.errs, LatencyUs: m.latAvg(), UA: c.ua, Bot: c.bot, Last: m.last})
	}

	stream = s.snapshotStreamLocked(f, 100)

	tot = s.totalsLocked()
	bad = s.bad
	s.mu.Unlock()
	sortRows(hostsRows, sorts[0])
	sortRows(urlRows, sorts[1])
	sortRows(clientRows, sorts[2])
	for i := range urlRows {
		r := &urlRows[i]
		r.Key = r.Vhost + " " + r.Method + " " + r.Path
	}
	s.mu.Lock()
	if s.generation == generation {
		s.cache = &snapshotCache{key: key, generation: generation, hosts: hostsRows, urls: urlRows, clients: clientRows, stream: stream}
	}
	s.mu.Unlock()
	return hostsRows, urlRows, clientRows, stream, tot, bad
}

// betterRow defines a total, deterministic ordering, including tied rows.
func betterRow(a, b *Row, key SortKey) bool {
	var av, bv int64
	switch key {
	case SortHits:
		av, bv = a.Hits, b.Hits
	case SortErrs:
		av, bv = a.Errs, b.Errs
	case SortBytes:
		av, bv = a.Bytes, b.Bytes
	}
	if key != SortRate && av != bv {
		return av > bv
	}
	if a.Rate != b.Rate {
		return a.Rate > b.Rate
	}
	if a.Hits != b.Hits {
		return a.Hits > b.Hits
	}
	return a.Key < b.Key
}

func sortRows(rows []Row, key SortKey) {
	sort.Slice(rows, func(i, j int) bool { return betterRow(&rows[i], &rows[j], key) })
}

// offerRow retains only the best n immutable rows in a worst-first heap.
// Its storage belongs to this snapshot; UI rows are never reused/mutated.
func offerRow(rows []Row, n int, key SortKey, r Row) []Row {
	if n <= 0 {
		return rows
	}
	if len(rows) < n {
		rows = append(rows, r)
		for i := len(rows) - 1; i > 0; {
			p := (i - 1) / 2
			if !betterRow(&rows[p], &rows[i], key) {
				break
			}
			rows[p], rows[i] = rows[i], rows[p]
			i = p
		}
		return rows
	}
	if !betterRow(&r, &rows[0], key) {
		return rows
	}
	rows[0] = r
	for i := 0; ; {
		child := i*2 + 1
		if child >= len(rows) {
			break
		}
		if child+1 < len(rows) && betterRow(&rows[child], &rows[child+1], key) {
			child++
		}
		if !betterRow(&rows[i], &rows[child], key) {
			break
		}
		rows[i], rows[child] = rows[child], rows[i]
		i = child
	}
	return rows
}

// StreamMatch reports whether a raw record passes every active filter
// exactly (the stream carries full records, so all dimensions are exact).
func StreamMatch(r parser.Record, f Filters) bool {
	if len(f.Hosts) > 0 && !f.Hosts[r.Vhost] {
		return false
	}
	if len(f.Clients) > 0 && !f.Clients[r.IP] {
		return false
	}
	if len(f.Paths) > 0 && !f.Paths[r.Path] {
		return false
	}
	if !f.Mask.Allows(r.Status) {
		return false
	}
	if f.Method != "" && r.Method != f.Method {
		return false
	}
	if f.Bots == +1 && !r.Bot {
		return false
	}
	if f.Bots == -1 && r.Bot {
		return false
	}
	if f.Static == -1 && r.Static {
		return false
	}
	return true
}

func (s *Store) snapshotStreamLocked(f Filters, max int) []parser.Record {
	out := make([]parser.Record, 0, 64)
	for i := 1; i <= s.streamLen && len(out) < max; i++ {
		var r parser.Record
		if s.streamLen < streamCap {
			r = s.stream[s.streamLen-i]
		} else {
			r = s.stream[(s.streamHead-i+streamCap)%streamCap]
		}
		if !StreamMatch(r, f) {
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
