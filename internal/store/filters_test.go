package store

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/steamvogue/wstat/internal/parser"
)

func TestJointFiltersAgainstRecords(t *testing.T) {
	s := New()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	var records []parser.Record
	for _, status := range []int{100, 200, 302, 404, 503} {
		for _, bot := range []bool{false, true} {
			for _, static := range []bool{false, true} {
				for _, host := range []string{"a", "b"} {
					for _, ip := range []string{"ip1", "ip2"} {
						for _, method := range []string{"GET", "POST"} {
							r := rec(host, ip, method, "/page", status, int64(len(records)+1))
							r.Bot = bot
							r.Static = static
							r.LatencyUs = int64(len(records)+1) * 10
							records = append(records, r)
							s.Add(r)
						}
					}
				}
			}
		}
	}
	for mask := StatusMask(0); mask < 16; mask++ {
		for _, bots := range []int8{0, 1, -1} {
			for _, static := range []int8{0, -1} {
				for _, hosts := range []map[string]bool{nil, {"a": true}, {"b": true}, {"a": true, "b": true}} {
					for _, method := range []string{"", "GET", "POST"} {
						f := Filters{Mask: mask, Bots: bots, Static: static, Hosts: hosts, Method: method, Clients: map[string]bool{"ip1": true}, Paths: map[string]bool{"/page": true}}
						h, u, c, stream, tot, _ := s.Snapshot(f, [3]SortKey{}, 1000)
						for panel, rows := range [][]Row{h, u, c} {
							expected := map[string]metric{}
							for _, r := range records {
								pf := Filters{Mask: mask, Bots: bots, Static: static}
								if panel != 0 {
									pf.Hosts = hosts
								}
								if panel == 1 {
									pf.Method = method
								}
								if !referenceMatch(r, pf) {
									continue
								}
								key := r.Vhost
								if panel == 1 {
									key = r.Vhost + " " + r.Method + " " + r.Path
								}
								if panel == 2 {
									key = r.IP
								}
								m := expected[key]
								m.hits++
								m.bytes += r.Bytes
								if r.Status >= 400 && r.Status <= 599 {
									m.errs++
								}
								if r.LatencyUs > 0 {
									m.latSum += r.LatencyUs
									m.latCount++
								}
								expected[key] = m
							}
							if len(rows) != len(expected) {
								t.Fatalf("panel %d filter %+v: %d rows, want %d", panel, f, len(rows), len(expected))
							}
							for _, row := range rows {
								want := expected[row.Key]
								if row.Hits != want.hits || row.Bytes != want.bytes || row.Errs != want.errs || row.LatencyUs != want.latAvg() || math.Abs(row.Rate-float64(want.hits)*ewmaAlpha) > 1e-9 {
									t.Fatalf("panel=%d filter=%+v row=%+v want=%+v", panel, f, row, want)
								}
								if row.Errs > row.Hits {
									t.Fatal("impossible error percentage")
								}
							}
						}
						wantStream := 0
						for _, r := range records {
							if referenceMatch(r, f) {
								wantStream++
							}
						}
						if len(stream) != min(100, wantStream) || tot.Reqs != int64(len(records)) {
							t.Fatal("stream/global scope mismatch")
						}
					}
				}
			}
		}
	}
}

func TestAdmissionLRUAndLifetimeTotals(t *testing.T) {
	s := New()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < maxKeys; i++ {
		s.AddSeed(rec("a", fmt.Sprint(i), "GET", fmt.Sprintf("/%d", i), 200, 1))
		now = now.Add(time.Microsecond)
	}
	s.AddSeed(rec("a", "0", "GET", "/0", 200, 1)) // oldest becomes most recent
	s.AddSeed(rec("a", "new", "GET", "/new", 200, 1))
	if s.urls["a\x00GET\x00/0"] == nil || s.clients["0"] == nil || s.urls["a\x00GET\x00/new"] == nil || s.clients["new"] == nil {
		t.Fatal("new/hot rows missing")
	}
	if s.urls["a\x00GET\x00/1"] != nil || s.clients["1"] != nil {
		t.Fatal("wrong LRU victim")
	}
	if len(s.urls) != maxKeys || len(s.clients) != maxKeys || s.tot.reqs != maxKeys+2 {
		t.Fatal("bounds or lifetime totals changed")
	}
	now = now.Add(3 * time.Minute)
	s.AddSeed(rec("a", "fresh", "GET", "/fresh", 200, 1))
	if len(s.urls) != 1 || len(s.clients) != 1 || s.tot.reqs != maxKeys+3 {
		t.Fatal("stale admission/totals")
	}
	_, _, _, _, tot, _ := snap(s)
	if tot.URLEvicted != maxKeys+1 || tot.ClientEvicted != maxKeys+1 {
		t.Fatal(tot)
	}
}

func TestHostAssociationBounds(t *testing.T) {
	s := New()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < maxHosts+10; i++ {
		s.AddSeed(rec(fmt.Sprint(i), "one-client", "GET", "/", 200, 1))
		now = now.Add(time.Microsecond)
	}
	if len(s.hosts) != maxHosts || len(s.clients["one-client"].vhosts) != maxClientHosts || len(s.pairs) != maxClientHosts {
		t.Fatal("unbounded hosts/associations")
	}
	if s.hosts[fmt.Sprint(maxHosts+9)] == nil {
		t.Fatal("new host not admitted")
	}
	_, _, clients, _, tot, _ := s.Snapshot(Filters{Hosts: map[string]bool{fmt.Sprint(maxHosts + 9): true}}, [3]SortKey{}, 200)
	if len(clients) != 1 || clients[0].Hits != 1 || tot.HostEvicted != 10 || tot.AssociationEvicted == 0 {
		t.Fatal(clients, tot)
	}
}

func TestGlobalAssociationBudgetKeepsNewDetail(t *testing.T) {
	s := New()
	for host := 0; host < 50; host++ {
		for client := 0; client < 1000; client++ {
			s.AddSeed(rec(fmt.Sprintf("host-%d", host), fmt.Sprintf("client-%d", client), "GET", "/", 200, 1))
		}
	}
	if len(s.pairs) != maxAssociations {
		t.Fatalf("association budget: got %d want %d", len(s.pairs), maxAssociations)
	}
	for key, client := range s.clients {
		if len(client.vhosts) > maxClientHosts || client.vhosts["host-49"] == nil {
			t.Fatal("client budget or newest association lost", key)
		}
	}
	_, _, clients, _, tot, _ := s.Snapshot(Filters{Hosts: map[string]bool{"host-49": true}}, [3]SortKey{}, 1000)
	if len(clients) != 1000 || tot.Reqs != 50000 || tot.AssociationEvicted != 50000-maxAssociations {
		t.Fatal("new detail or lifetime totals lost", len(clients), tot)
	}
}

// Evaluate full records independently of the aggregate implementation.
func referenceMatch(r parser.Record, f Filters) bool {
	if len(f.Hosts) > 0 && !f.Hosts[r.Vhost] || len(f.Clients) > 0 && !f.Clients[r.IP] || len(f.Paths) > 0 && !f.Paths[r.Path] || f.Method != "" && f.Method != r.Method {
		return false
	}
	if f.Mask != 0 {
		class := r.Status / 100
		if class < 2 || class > 5 || uint8(f.Mask)&(1<<uint(class-2)) == 0 {
			return false
		}
	}
	if f.Bots > 0 && !r.Bot || f.Bots < 0 && r.Bot || f.Static < 0 && r.Static {
		return false
	}
	return true
}

func TestRateDecayAndSeedOnlyInactivity(t *testing.T) {
	s := New()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.AddSeed(rec("seed", "seed", "GET", "/seed", 200, 1))
	if len(s.rateActive) != 0 {
		t.Fatal("history registered live rate work")
	}
	for i := 0; i < 10; i++ {
		s.Add(botRec("a", "ip", "GET", "/", 500, 1))
	}
	f := Filters{Hosts: map[string]bool{"a": true}, Bots: 1, Mask: MaskErr}
	h, _, c, _, tot, _ := s.Snapshot(f, [3]SortKey{}, 200)
	if h[0].Rate != 4 || c[0].Rate != 4 || tot.Rate != 4 {
		t.Fatal(h, c, tot)
	}
	now = now.Add(500 * time.Millisecond)
	h, _, c, _, tot, _ = s.Snapshot(f, [3]SortKey{}, 200)
	if math.Abs(h[0].Rate-2.4) > 1e-9 || math.Abs(c[0].Rate-2.4) > 1e-9 || math.Abs(tot.Rate-2.4) > 1e-9 {
		t.Fatal(h, c, tot)
	}
	for i := 0; i < 60; i++ {
		now = now.Add(500 * time.Millisecond)
		s.Snapshot(f, [3]SortKey{}, 200)
	}
	if len(s.rateActive) != 0 {
		t.Fatal("idle rate work never retired")
	}
}
