package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"wstat/internal/logsrc"
	"wstat/internal/parser"
	"wstat/internal/store"
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
	m.hostSel = map[string]bool{"dev.local": true}
	m = drive(m, tickMsg(time.Now()))
	out := m.render()
	if strings.Contains(out, "cms.local") == false {
		// hosts panel must still show cms.local even when filtered
		t.Error("hosts panel must not be hidden by host filter")
	}

	// Status filter to errors only.
	m.status = store.MaskErr
	m = drive(m, tickMsg(time.Now()))
	if !m.status.Allows(500) || m.status.Allows(200) {
		t.Error("status mask broken")
	}

	// Clear with X.
	m = drive(m, tea.KeyPressMsg{Text: "X"}, tickMsg(time.Now()))
	if len(m.hostSel) != 0 || m.status != 0 {
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
