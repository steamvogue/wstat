package store

import (
	"sort"
	"strconv"
	"strings"

	"github.com/steamvogue/wstat/internal/parser"
)

// One immutable snapshot is retained. Callers may read published rows but must
// not mutate them; ingestion and later snapshots never reuse their storage.
type snapshotCache struct {
	key                  snapshotKey
	generation           uint64
	hosts, urls, clients []Row
	stream               []parser.Record
}
type snapshotKey struct {
	hosts, clients, paths, method string
	mask                          StatusMask
	bots, static                  int8
	sorts                         [3]SortKey
	topN                          int
}

func filterSetKey(set map[string]bool) string {
	if len(set) == 0 {
		return ""
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(strconv.Quote(k))
		if set[k] {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}
func cacheKey(f Filters, sorts [3]SortKey, topN int) snapshotKey {
	return snapshotKey{hosts: filterSetKey(f.Hosts), clients: filterSetKey(f.Clients), paths: filterSetKey(f.Paths), method: f.Method, mask: f.Mask, bots: f.Bots, static: f.Static, sorts: sorts, topN: topN}
}
func (s *Store) totalsLocked() Totals {
	return Totals{Reqs: s.tot.reqs, Bytes: s.tot.bytes, Class: s.tot.class, Bots: s.tot.bots, Rate: s.tot.rate, ByteRate: s.tot.byteRate, TrackedHosts: len(s.hosts), HostEvicted: s.hostOverflow, URLEvicted: s.urlOverflow, ClientEvicted: s.clientOverflow, AssociationEvicted: s.pairOverflow}
}
