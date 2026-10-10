package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/steamvogue/wstat/internal/parser"
)

func rec(vhost, ip, method, path string, status int, bytes int64) parser.Record {
	return parser.Record{
		Vhost: vhost, IP: ip, Method: method, Path: path,
		Status: status, Bytes: bytes, Time: time.Now(),
		UA: "Mozilla/5.0 test-agent",
	}
}

func botRec(vhost, ip, method, path string, status int, bytes int64) parser.Record {
	r := rec(vhost, ip, method, path, status, bytes)
	r.Bot = true
	r.UA = "curl/8.5.0"
	return r
}

func TestStreamIDsDistinguishIdenticalRequestsAcrossRollover(t *testing.T) {
	s := New()
	r := rec("test", "client", "GET", "/same", 200, 1)
	r.StreamID = 999 // Caller metadata cannot collide with Store-assigned IDs.
	for i := 0; i < streamCap+120; i++ {
		s.AddSeed(r)
	}
	_, _, _, stream, _, _ := snap(s)
	for i, row := range stream {
		want := uint64(streamCap + 120 - len(stream) + i + 1)
		if row.StreamID != want {
			t.Fatalf("stream row %d ID=%d, want %d", i, row.StreamID, want)
		}
	}
	lastID := stream[len(stream)-1].StreamID
	s.Add(r)
	_, _, _, next, _, _ := snap(s)
	if next[len(next)-1].StreamID != lastID+1 || stream[len(stream)-1].StreamID != lastID || r.StreamID != 999 {
		t.Fatal("live insertion reused an ID or mutated a previous snapshot/caller record")
	}
}

func allFilters() Filters { return Filters{} }

func snap(s *Store) ([]Row, []Row, []Row, []parser.Record, Totals, int64) {
	return s.Snapshot(allFilters(), [3]SortKey{SortRate, SortHits, SortRate}, 50)
}

func TestSnapshotBasics(t *testing.T) {
	s := New()
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 100))
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 500, 50))
	s.Add(rec("bison.net", "10.0.0.2", "POST", "/y", 404, 10))

	hosts, urls, clients, stream, tot, bad := snap(s)
	if len(hosts) != 2 {
		t.Fatalf("hosts = %d, want 2", len(hosts))
	}
	if hosts[0].Key != "alpaca.org" || hosts[0].Hits != 2 {
		t.Errorf("top host = %+v, want alpaca.org hits=2", hosts[0])
	}
	if hosts[0].Errs != 1 {
		t.Errorf("alpaca.org errs = %d, want 1", hosts[0].Errs)
	}
	if len(urls) != 2 {
		t.Fatalf("urls = %d, want 2 (a/x aggregated + b/y)", len(urls))
	}
	if len(clients) != 2 {
		t.Fatalf("clients = %d, want 2", len(clients))
	}
	if len(stream) != 3 {
		t.Fatalf("stream = %d, want 3", len(stream))
	}
	if tot.Reqs != 3 || tot.Bytes != 160 || tot.Class[0] != 1 || tot.Class[2] != 1 || tot.Class[3] != 1 {
		t.Errorf("totals = %+v", tot)
	}
	if bad != 0 {
		t.Errorf("bad = %d", bad)
	}
}

func TestHostFilterCrossFilterSemantics(t *testing.T) {
	s := New()
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 1))
	s.Add(rec("bison.net", "10.0.0.2", "GET", "/y", 200, 1))

	f := Filters{Hosts: map[string]bool{"alpaca.org": true}}
	hosts, urls, clients, stream, _, _ := s.Snapshot(f, [3]SortKey{SortRate, SortHits, SortRate}, 50)

	// Hosts panel is NOT scoped by the host filter (selection marking is UI).
	if len(hosts) != 2 {
		t.Errorf("hosts panel must show all hosts, got %d", len(hosts))
	}
	if len(urls) != 1 || urls[0].Vhost != "alpaca.org" {
		t.Errorf("urls = %+v, want only alpaca.org", urls)
	}
	if len(clients) != 1 || clients[0].Key != "10.0.0.1" {
		t.Errorf("clients = %+v, want only 10.0.0.1", clients)
	}
	if len(stream) != 1 || stream[0].Vhost != "alpaca.org" {
		t.Errorf("stream = %+v, want only alpaca.org", stream)
	}
}

func TestStatusMask(t *testing.T) {
	s := New()
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 1))
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 404, 1))
	s.Add(rec("alpaca.org", "10.0.0.2", "GET", "/z", 500, 1))

	f := Filters{Mask: MaskErr}
	hosts, urls, _, stream, _, _ := s.Snapshot(f, [3]SortKey{SortRate, SortHits, SortRate}, 50)
	if hosts[0].Hits != 2 {
		t.Errorf("host hits under err mask = %d, want 2", hosts[0].Hits)
	}
	if len(stream) != 2 {
		t.Errorf("stream under err mask = %d, want 2", len(stream))
	}
	if len(urls) != 2 {
		t.Errorf("urls under err mask = %d, want 2", len(urls))
	}

	_, _, _, streamAll, _, _ := s.Snapshot(allFilters(), [3]SortKey{SortRate, SortHits, SortRate}, 50)
	if len(streamAll) != 3 {
		t.Errorf("stream without mask = %d, want 3", len(streamAll))
	}
}

func TestStatusMaskAllows(t *testing.T) {
	var noMask StatusMask
	if !noMask.Allows(200) || !MaskErr.Allows(404) || MaskErr.Allows(200) {
		t.Error("mask semantics broken")
	}
}

func TestBotsFilter(t *testing.T) {
	s := New()
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 1))
	s.Add(botRec("alpaca.org", "10.0.0.2", "GET", "/x", 200, 1))
	s.Add(botRec("bison.net", "10.0.0.3", "GET", "/y", 200, 1))

	// Bots only: exact everywhere (1 bot hit per host).
	f := Filters{Bots: +1}
	hosts, urls, clients, stream, _, _ := s.Snapshot(f, [3]SortKey{SortRate, SortHits, SortRate}, 50)
	if len(hosts) != 2 || hosts[0].Hits != 1 {
		t.Errorf("bots-only hosts = %+v", hosts)
	}
	if len(stream) != 2 {
		t.Errorf("bots-only stream = %d, want 2", len(stream))
	}
	_ = urls
	_ = clients

	// Humans only.
	f.Bots = -1
	_, _, _, streamH, _, _ := s.Snapshot(f, [3]SortKey{SortRate, SortHits, SortRate}, 50)
	if len(streamH) != 1 || streamH[0].IP != "10.0.0.1" {
		t.Errorf("humans-only stream = %+v", streamH)
	}
}

func TestStaticFilter(t *testing.T) {
	s := New()
	r1 := rec("alpaca.org", "10.0.0.1", "GET", "/style.css", 200, 1)
	r1.Static = true
	r2 := rec("alpaca.org", "10.0.0.1", "GET", "/page", 200, 1)
	s.Add(r1)
	s.Add(r2)

	f := Filters{Static: -1}
	hosts, urls, _, stream, _, _ := s.Snapshot(f, [3]SortKey{SortRate, SortHits, SortRate}, 50)
	if hosts[0].Hits != 1 {
		t.Errorf("no-static host hits = %d, want 1", hosts[0].Hits)
	}
	if len(urls) != 1 || urls[0].Path != "/page" {
		t.Errorf("no-static urls = %+v", urls)
	}
	if len(stream) != 1 || stream[0].Path != "/page" {
		t.Errorf("no-static stream = %+v", stream)
	}
}

func TestMethodFilter(t *testing.T) {
	s := New()
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 1))
	s.Add(rec("alpaca.org", "10.0.0.1", "POST", "/x", 200, 1))

	f := Filters{Method: "POST"}
	_, urls, _, stream, _, _ := s.Snapshot(f, [3]SortKey{SortRate, SortHits, SortRate}, 50)
	if len(urls) != 1 || urls[0].Method != "POST" {
		t.Errorf("method urls = %+v", urls)
	}
	if len(stream) != 1 || stream[0].Method != "POST" {
		t.Errorf("method stream = %+v", stream)
	}
}

func TestClientAndPathFiltersOnStream(t *testing.T) {
	s := New()
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 1))
	s.Add(rec("bison.net", "10.0.0.2", "GET", "/y", 200, 1))

	f := Filters{Clients: map[string]bool{"10.0.0.2": true}, Paths: map[string]bool{"/y": true}}
	_, _, _, stream, _, _ := s.Snapshot(f, [3]SortKey{SortRate, SortHits, SortRate}, 50)
	if len(stream) != 1 || stream[0].IP != "10.0.0.2" || stream[0].Path != "/y" {
		t.Errorf("client+path stream = %+v", stream)
	}
}

func TestSortKeys(t *testing.T) {
	s := New()
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 10))
	s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 404, 10))
	s.Add(rec("bison.net", "10.0.0.2", "GET", "/y", 200, 9999))

	hosts, _, _, _, _, _ := s.Snapshot(allFilters(), [3]SortKey{SortErrs, SortHits, SortRate}, 50)
	if hosts[0].Key != "alpaca.org" {
		t.Errorf("errs sort top = %+v, want alpaca.org", hosts[0])
	}
	_, _, _, _, _, _ = s.Snapshot(allFilters(), [3]SortKey{SortBytes, SortHits, SortRate}, 50)
}

func TestRateFlush(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		s.Add(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 1))
	}
	hosts, _, _, _, tot, _ := s.Snapshot(allFilters(), [3]SortKey{SortRate, SortHits, SortRate}, 10)
	if hosts[0].Rate <= 0 {
		t.Errorf("rate after burst = %v, want > 0", hosts[0].Rate)
	}
	if tot.Rate <= 0 {
		t.Errorf("global rate = %v, want > 0", tot.Rate)
	}
}

func TestSeedDoesNotFeedRates(t *testing.T) {
	s := New()
	for i := 0; i < 500; i++ {
		s.AddSeed(rec("alpaca.org", "10.0.0.1", "GET", "/x", 200, 1))
	}
	hosts, _, _, _, tot, _ := s.Snapshot(allFilters(), [3]SortKey{SortRate, SortHits, SortRate}, 10)
	if hosts[0].Rate != 0 || tot.Rate != 0 {
		t.Errorf("seeded lines must not feed rates: host=%v total=%v", hosts[0].Rate, tot.Rate)
	}
	if hosts[0].Hits != 500 {
		t.Errorf("seeded hits = %d, want 500", hosts[0].Hits)
	}
}

func TestStreamRingBounded(t *testing.T) {
	s := New()
	for i := 0; i < streamCap+50; i++ {
		s.Add(rec("alpaca.org", "10.0.0.1", "GET", fmt.Sprintf("/p%d", i), 200, 1))
	}
	_, _, _, stream, _, _ := s.Snapshot(allFilters(), [3]SortKey{SortRate, SortHits, SortRate}, 1000)
	if len(stream) > streamCap {
		t.Fatalf("stream len = %d, cap %d", len(stream), streamCap)
	}
	last := stream[len(stream)-1]
	if last.Path != fmt.Sprintf("/p%d", streamCap+49) {
		t.Errorf("newest = %q", last.Path)
	}
}
