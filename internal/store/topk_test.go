package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestTopKMatchesFullSort(t *testing.T) {
	for _, key := range []SortKey{SortRate, SortHits, SortErrs, SortBytes} {
		for _, n := range []int{0, 1, 17, 200, 300} {
			all := make([]Row, 250)
			var top []Row
			for i := range all {
				r := Row{Key: fmt.Sprintf("%03d", i), Hits: int64(i % 13), Errs: int64(i % 7), Bytes: int64(i % 19), Rate: float64(i % 3)}
				all[i] = r
				top = offerRow(top, n, key, r)
			}
			sortRows(all, key)
			sortRows(top, key)
			if len(top) != min(n, len(all)) || !reflect.DeepEqual(top, all[:len(top)]) && len(top) > 0 {
				t.Fatalf("sort=%v n=%d: top-K differs", key, n)
			}
		}
	}
}

func TestSnapshotImmutableAndZero(t *testing.T) {
	s := New()
	s.Add(rec("a", "ip", "GET", "/", 200, 1))
	h, u, c, _, _, _ := s.Snapshot(Filters{}, [3]SortKey{}, 0)
	if len(h)+len(u)+len(c) != 0 {
		t.Fatal("zero requested rows")
	}
	h, _, _, _, _, _ = snap(s)
	s.Add(rec("a", "ip", "GET", "/", 500, 9))
	snap(s)
	if h[0].Hits != 1 || h[0].Bytes != 1 {
		t.Fatal("published snapshot mutated")
	}
}
