package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/parser"
	"github.com/steamvogue/wstat/internal/store"
)

func mkModel(t *testing.T) Model {
	st := store.New()
	for i := 0; i < 30; i++ {
		st.Add(parser.Record{
			Vhost: "cms.local", IP: "10.0.0.1", Method: "GET", Path: "/panel/login",
			Status: 200, Bytes: 4096, Time: time.Now(), UA: "Mozilla/5.0 test",
		})
		st.Add(parser.Record{
			Vhost: "dev.local", IP: "10.0.0.2", Method: "POST", Path: "/api/nodes",
			Status: 500, Bytes: 128, Time: time.Now(), UA: "curl/8.5.0",
		})
	}
	tailer := logsrc.Start(nil, 0) // no files; used only for source count
	defer tailer.Stop()
	m := New(st, tailer)
	return m
}

func drive(m Model, msgs ...any) Model {
	for _, msg := range msgs {
		next, cmd := m.Update(msg)
		m = next.(Model)
		_ = cmd
	}
	return m
}

func TestViewRenders(t *testing.T) {
	m := mkModel(t)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tickMsg(time.Now()))
	out := m.render()
	for _, want := range []string{"HOSTS", "TOP URLS", "CLIENTS", "LIVE REQUESTS", "cms.local", "dev.local", "req/s"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
	if strings.Count(out, "\n") > 41 {
		t.Errorf("render too tall: %d lines", strings.Count(out, "\n"))
	}
}

func TestFilterInteractions(t *testing.T) {
	m := mkModel(t)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tickMsg(time.Now()))

	// Select dev.local in hosts (sorted by rate; find it by toggling each row).
	// Simpler: set filter directly then verify cross-filter semantics.
	m.filters.Hosts = map[string]bool{"dev.local": true}
	m = drive(m, tickMsg(time.Now()))
	out := m.render()
	if strings.Contains(out, "cms.local") == false {
		// hosts panel must still show cms.local even when filtered
		t.Error("hosts panel must not be hidden by host filter")
	}

	// Status filter to errors only.
	m.filters.Mask = store.MaskErr
	m = drive(m, tickMsg(time.Now()))
	if !m.filters.Mask.Allows(500) || m.filters.Mask.Allows(200) {
		t.Error("status mask broken")
	}

	// Clear with X.
	m = drive(m, tea.KeyPressMsg{Text: "X"}, tickMsg(time.Now()))
	if m.filters.Any() {
		t.Error("X must clear all filters")
	}
}

func TestSearchMode(t *testing.T) {
	m := mkModel(t)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tickMsg(time.Now()))
	m = drive(m, tea.KeyPressMsg{Text: "/"})
	if !m.searchMode {
		t.Fatal("expected search mode")
	}
	// 'q' must be typed, not quit.
	m = drive(m, tea.KeyPressMsg{Text: "q"})
	if m.searchMode != true {
		t.Fatal("q in search mode must not quit")
	}
	if m.search != "q" {
		t.Fatalf("search = %q", m.search)
	}
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.searchMode || m.search != "" {
		t.Fatal("esc must cancel search")
	}
}

func TestZoomToggle(t *testing.T) {
	m := mkModel(t)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tickMsg(time.Now()), tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.zoom {
		t.Fatal("enter must toggle zoom")
	}
	out := m.render()
	if strings.Contains(out, "HOSTS") == false && strings.Contains(out, "TOP URLS") == false {
		t.Error("zoom must render the focused panel")
	}
}

func TestFilterKeyCycles(t *testing.T) {
	m := mkModel(t)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tickMsg(time.Now()))

	m = drive(m, tea.KeyPressMsg{Text: "x"})
	if m.filters.Mask != store.MaskErr {
		t.Errorf("x: mask = %v, want MaskErr", m.filters.Mask)
	}
	m = drive(m, tea.KeyPressMsg{Text: "m"})
	if m.filters.Method != "GET" {
		t.Errorf("m: method = %q, want GET", m.filters.Method)
	}
	m = drive(m, tea.KeyPressMsg{Text: "b"})
	if m.filters.Bots != +1 {
		t.Errorf("b: bots = %d, want +1", m.filters.Bots)
	}
	m = drive(m, tea.KeyPressMsg{Text: "t"})
	if m.filters.Static != -1 {
		t.Errorf("t: static = %d, want -1", m.filters.Static)
	}
	chips := strings.Join(m.chips(), " ")
	for _, want := range []string{"4xx-5xx", "GET", "bots", "no-static"} {
		if !strings.Contains(chips, want) {
			t.Errorf("chips missing %q: %v", want, chips)
		}
	}
	// X clears everything at once.
	m = drive(m, tea.KeyPressMsg{Text: "X"})
	if m.filters.Any() {
		t.Errorf("X must clear all filters, left %+v", m.filters)
	}
}

func TestSortAndFreeze(t *testing.T) {
	m := mkModel(t)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tickMsg(time.Now()))
	m = drive(m, tea.KeyPressMsg{Text: "s"})
	if m.sorts[0] != store.SortHits {
		t.Errorf("s: sorts[0] = %v, want hits", m.sorts[0])
	}
	out := m.render()
	if !strings.Contains(out, "hits") {
		t.Error("sort marker missing from title")
	}
	m = drive(m, tea.KeyPressMsg{Text: "f"})
	if !m.frozen {
		t.Error("f must freeze the stream")
	}
	m = drive(m, tea.KeyPressMsg{Text: "T"})
	if m.theme != 1 {
		t.Errorf("T: theme = %d, want 1", m.theme)
	}
	m = drive(m, tea.KeyPressMsg{Text: "T"}, tea.KeyPressMsg{Text: "T"})
	if m.theme != 0 {
		t.Errorf("theme cycle broken: %d", m.theme)
	}
}

func TestFuzzySearch(t *testing.T) {
	if !fuzzy("pl", "/panel/login") || !fuzzy("PL", "/panel/login") {
		t.Error("fuzzy subsequence match broken")
	}
	if fuzzy("xyz", "/panel/login") {
		t.Error("fuzzy must not match unrelated needle")
	}
}
