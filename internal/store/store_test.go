package store

import (
	"fmt"
	"testing"
	"time"

	"wstat/internal/parser"
)

func rec(vhost, ip, method, path string, status int, bytes int64) parser.Record {
	return parser.Record{
		Vhost: vhost, IP: ip, Method: method, Path: path,
		Status: status, Bytes: bytes, Time: time.Now(),
		UA: "Mozilla/5.0 test-agent",
	}
}

func TestSnapshotBasics(t *testing.T) {
	s := New()
	s.Add(rec("a.local", "10.0.0.1", "GET", "/x", 200, 100))
	s.Add(rec("a.local", "10.0.0.1", "GET", "/x", 500, 50))
	s.Add(rec("b.local", "10.0.0.2", "POST", "/y", 404, 10))

	hosts, urls, clients, stream, tot, bad := s.Snapshot(nil, 0, 50)
	if len(hosts) != 2 {
		t.Fatalf("hosts = %d, want 2", len(hosts))
	}
	if hosts[0].Key != "a.local" || hosts[0].Hits != 2 {
		t.Errorf("top host = %+v, want a.local hits=2", hosts[0])
	}
	if hosts[0].Errs != 1 {
		t.Errorf("a.local errs = %d, want 1", hosts[0].Errs)
	}
	if len(urls) != 2 {
		t.Fatalf("urls = %d, want 2 (a/x aggregated, b/y)", len(urls))
	}
	if urls[0].Hits != 2 {
		t.Errorf("top url hits = %d, want 2", urls[0].Hits)
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
	s.Add(rec("a.local", "10.0.0.1", "GET", "/x", 200, 1))
	s.Add(rec("b.local", "10.0.0.2", "GET", "/y", 200, 1))

	sel := map[string]bool{"a.local": true}
	hosts, urls, clients, stream, _, _ := s.Snapshot(sel, 0, 50)

	// Hosts panel is NOT scoped by the host filter (selection marking is UI).
	if len(hosts) != 2 {
		t.Errorf("hosts panel must show all hosts, got %d", len(hosts))
	}
	if len(urls) != 1 || urls[0].Vhost != "a.local" {
		t.Errorf("urls = %+v, want only a.local", urls)
	}
	if len(clients) != 1 || clients[0].Key != "10.0.0.1" {
		t.Errorf("clients = %+v, want only 10.0.0.1", clients)
	}
	if len(stream) != 1 || stream[0].Vhost != "a.local" {
		t.Errorf("stream = %+v, want only a.local", stream)
	}
}

func TestStatusMask(t *testing.T) {
	s := New()
	s.Add(rec("a.local", "10.0.0.1", "GET", "/x", 200, 1))
	s.Add(rec("a.local", "10.0.0.1", "GET", "/x", 404, 1))
	s.Add(rec("a.local", "10.0.0.2", "GET", "/z", 500, 1))

	hosts, urls, clients, stream, _, _ := s.Snapshot(nil, MaskErr, 50)
	if hosts[0].Hits != 2 {
		t.Errorf("host hits under err mask = %d, want 2", hosts[0].Hits)
	}
	if len(stream) != 2 {
		t.Errorf("stream under err mask = %d, want 2", len(stream))
	}
	if len(urls) != 2 {
		t.Errorf("urls under err mask = %d, want 2", len(urls))
	}
	_ = clients

	_, _, _, streamAll, _, _ := s.Snapshot(nil, 0, 50)
	if len(streamAll) != 3 {
		t.Errorf("stream without mask = %d, want 3", len(streamAll))
	}
}

func TestStatusMaskAllows(t *testing.T) {
	if !Mask(0).Allows(200) || !MaskErr.Allows(404) || MaskErr.Allows(200) {
		t.Error("mask semantics broken")
	}
}

type Mask = StatusMask

func TestRateFlush(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		s.Add(rec("a.local", "10.0.0.1", "GET", "/x", 200, 1))
	}
	hosts, _, _, _, tot, _ := s.Snapshot(nil, 0, 10)
	if hosts[0].Rate <= 0 {
		t.Errorf("rate after burst = %v, want > 0", hosts[0].Rate)
	}
	if tot.Rate <= 0 {
		t.Errorf("global rate = %v, want > 0", tot.Rate)
	}
}

func TestStreamRingBounded(t *testing.T) {
	s := New()
	for i := 0; i < streamCap+50; i++ {
		s.Add(rec("a.local", "10.0.0.1", "GET", fmt.Sprintf("/p%d", i), 200, 1))
	}
	_, _, _, stream, _, _ := s.Snapshot(nil, 0, 1000)
	if len(stream) > streamCap {
		t.Fatalf("stream len = %d, cap %d", len(stream), streamCap)
	}
	// Newest entry must be present and chronological order preserved.
	last := stream[len(stream)-1]
	if last.Path != fmt.Sprintf("/p%d", streamCap+49) {
		t.Errorf("newest = %q", last.Path)
	}
	for i := 1; i < len(stream); i++ {
		if stream[i-1].Time.After(stream[i].Time) && stream[i-1].Path == stream[i].Path {
			t.Fatal("stream not chronological")
		}
	}
}
