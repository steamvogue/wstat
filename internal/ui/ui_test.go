package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/steamvogue/wstat/internal/fpm"
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
	tailer := logsrc.Start(nil, nil, 0, nil) // no files; used only for source count
	defer tailer.Stop()
	m := New(st, tailer, nil)
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
	if lipgloss.Height(out) != m.height {
		t.Errorf("render height = %d, want %d", lipgloss.Height(out), m.height)
	}
}

func TestBottomHelpStaysVisible(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 24}, {120, 40}, {160, 44}} {
		for _, mode := range []string{"dashboard", "zoom", "services", "search"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], mode), func(t *testing.T) {
				m := drive(mkModel(t), tea.WindowSizeMsg{Width: size[0], Height: size[1]}, tickMsg(time.Now()))
				// Long filter chips used to wrap the header and push help off screen.
				m.filters.Hosts = map[string]bool{strings.Repeat("long-host", 40): true}
				m.zoom = mode == "zoom"
				if mode == "services" {
					m.view = 1
				}
				m.searchMode = mode == "search"
				m.search = strings.Repeat("search", 40)
				out := m.render()
				if lipgloss.Height(out) != m.height || lipgloss.Width(out) > m.width {
					t.Fatalf("render = %dx%d, terminal = %dx%d", lipgloss.Width(out), lipgloss.Height(out), m.width, m.height)
				}
				lines := strings.Split(out, "\n")
				last := lines[len(lines)-1]
				if m.searchMode {
					if !strings.Contains(last, "find:") {
						t.Fatal("search prompt missing from last terminal row")
					}
					return
				}
				for _, hint := range []string{"q quit", "Tab focus", "/ find", "v view"} {
					if !strings.Contains(last, hint) {
						t.Errorf("last terminal row missing %q", hint)
					}
				}
				if m.width == 160 {
					hints := []string{"F hosts", "↑↓ move", "⏎ zoom", "s sort", "X clear", "f stream", "h/c/p filter", "x/m/b/t filters", "T theme", "live:0"}
					if mode == "services" {
						hints = []string{"↑↓ move", "g/G ends", "⏎ zoom", "1 pools", "2 sources", "3/4 stream", "f stream", "live:0"}
					}
					for _, hint := range hints {
						if !strings.Contains(last, hint) {
							t.Errorf("wide footer missing %q", hint)
						}
					}
				}
			})
		}
	}
}

func TestHostsFreezeKeepsRowsWhileOtherPanelsUpdate(t *testing.T) {
	m := mkModel(t)
	m.sorts[0] = store.SortHits
	m.refresh()
	m.sel[0] = 1
	selected := m.hosts[1].Key
	before := append([]store.Row(nil), m.hosts...)
	m = drive(m, tea.KeyPressMsg{Text: "F"})
	for i := 0; i < 100; i++ {
		m.st.Add(parser.Record{Vhost: selected, IP: "new-client", Method: "GET", Path: "/new", Status: 200, Time: time.Now()})
	}
	m.st.Add(parser.Record{Vhost: "new-host", IP: "new-client", Method: "GET", Path: "/new", Status: 200, Time: time.Now()})
	m = drive(m, tickMsg(time.Now()))
	if !m.hostsFrozen || m.frozen || !reflect.DeepEqual(m.hosts, before) || m.sel[0] != 1 {
		t.Fatal("Hosts rows or selection changed while frozen, or stream was frozen")
	}
	if m.tot.Reqs != 161 || len(m.urls) != 4 || len(m.clients) != 3 || len(m.stream) == 0 {
		t.Fatalf("freezing Hosts stopped updates elsewhere: requests=%d URLs=%d clients=%d stream=%d", m.tot.Reqs, len(m.urls), len(m.clients), len(m.stream))
	}
	if !strings.Contains(m.panelTitle(0), "[frozen]") {
		t.Fatal("Hosts freeze indicator missing")
	}
	// Freeze stream separately; resuming Hosts must retain that setting.
	m = drive(m, tea.KeyPressMsg{Text: "f"}, tea.KeyPressMsg{Text: "F"})
	if m.hostsFrozen || !m.frozen || reflect.DeepEqual(m.hosts, before) || len(m.hosts) != 3 {
		t.Fatal("Hosts did not resume immediately, or changed stream freeze")
	}
	if m.hosts[m.sel[0]].Key != selected || strings.Contains(m.panelTitle(0), "[frozen]") {
		t.Fatal("resuming Hosts lost selection or kept freeze indicator")
	}
}

func TestHostsFilterAndSortChangesResumeUpdates(t *testing.T) {
	for _, key := range []string{"x", "b", "t", "s", "X"} {
		t.Run(key, func(t *testing.T) {
			m := mkModel(t)
			m.refresh()
			m = drive(m, tea.KeyPressMsg{Text: "F"}, tea.KeyPressMsg{Text: key})
			if m.hostsFrozen {
				t.Fatal("Hosts remained frozen after changing its display settings")
			}
		})
	}
	m := mkModel(t)
	m.refresh()
	m = drive(m, tea.KeyPressMsg{Text: "F"}, tea.KeyPressMsg{Text: "2"}, tea.KeyPressMsg{Text: "s"})
	if !m.hostsFrozen {
		t.Fatal("sorting URLs resumed Hosts")
	}
}

func TestBotFilterReturnsURLsToTopImmediately(t *testing.T) {
	s := store.New()
	add := func(path string, bot bool, count int) {
		for i := 0; i < count; i++ {
			s.AddSeed(parser.Record{Vhost: "test.local", IP: "client", Method: "GET", Path: path, Status: 200, Bot: bot, Time: time.Now()})
		}
	}
	add("/former-top", false, 250)
	add("/former-top", true, 1)
	for i := 1; i < 40; i++ {
		add(fmt.Sprintf("/bot-%02d", i), true, 200-i)
		add(fmt.Sprintf("/bot-%02d", i), false, 1)
	}
	m := New(s, nil, nil)
	m.focus, m.sorts[1] = 1, store.SortHits
	m.refresh()
	if m.urls[0].Path != "/former-top" {
		t.Fatal("fixture must start with a URL that drops to the bottom under the bot filter")
	}
	for i, want := range []string{"/bot-01", "/former-top", "/former-top"} {
		m = drive(m, tea.KeyPressMsg{Text: "b"})
		if m.focus != 1 || m.sel[1] != 0 || m.urls[0].Path != want {
			t.Errorf("bot cycle %d: focus=%d selection=%d top=%s, want %s immediately", i, m.focus, m.sel[1], m.urls[0].Path, want)
		}
		m = drive(m, tickMsg(time.Now()))
		if m.sel[1] != 0 || !strings.Contains(strings.Join(m.urlLines(100, 5), "\n"), want) {
			t.Fatalf("bot cycle %d: tick pushed the top URL out of view (selection=%d)", i, m.sel[1])
		}
		// Also reset when the user was already inspecting a lower row.
		m = drive(m, tea.KeyPressMsg{Text: "G"})
	}
}

func TestURLTopStaysAnchoredOnLiveReorder(t *testing.T) {
	s := store.New()
	add := func(path string, count int) {
		for i := 0; i < count; i++ {
			s.AddSeed(parser.Record{Vhost: "test.local", IP: "client", Method: "GET", Path: path, Status: 200, Time: time.Now()})
		}
	}
	add("/old-top", 10)
	for i := 0; i < 30; i++ {
		add(fmt.Sprintf("/url-%02d", i), 1)
	}
	m := New(s, nil, nil)
	m.focus, m.sorts[1] = 1, store.SortHits
	m.refresh()
	for i := 0; i < 30; i++ {
		add(fmt.Sprintf("/url-%02d", i), 20)
	}
	m = drive(m, tickMsg(time.Now()))
	if m.sel[1] != 0 || strings.Contains(strings.Join(m.urlLines(100, 5), "\n"), "/old-top") {
		t.Fatal("following the old top URL scrolled past the live leaders")
	}
}

func TestMissingSelectedURLReturnsToTop(t *testing.T) {
	s := store.New()
	for i := 0; i < 40; i++ {
		for _, method := range []string{"GET", "POST"} {
			s.AddSeed(parser.Record{Vhost: "test.local", IP: "client", Method: method, Path: fmt.Sprintf("/url-%02d", i), Status: 200, Time: time.Now()})
		}
	}
	m := New(s, nil, nil)
	m.refresh()
	m.sel[1] = 20 // GET sorts ahead of POST when metrics tie.
	if m.urls[m.sel[1]].Method != "GET" {
		t.Fatal("fixture must select a GET URL")
	}
	m.filters.Method = "POST"
	m.refresh()
	if m.sel[1] != 0 || len(m.urls) != 40 {
		t.Fatal("missing selection retained a stale scroll offset")
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

func TestServicesViewRenders(t *testing.T) {
	m := mkModel(t)
	m.fpmViews = func() []fpm.PoolView {
		return []fpm.PoolView{
			{Pool: fpm.Pool{Name: "www", PMMode: "dynamic"}, Status: &fpm.Status{
				ProcessManager: "dynamic", ActiveProcesses: 2, TotalProcesses: 3, ListenQueue: 0,
			}},
			{Pool: fpm.Pool{Name: "idle", PMMode: "ondemand"}, Disabled: true},
		}
	}
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40}, tickMsg(time.Now()), tea.KeyPressMsg{Text: "v"})
	out := m.render()
	for _, want := range []string{"PHP-FPM", "SOURCE HEALTH", "www", "idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("services view missing %q", want)
		}
	}
	if m.view != 1 {
		t.Errorf("view = %d, want 1", m.view)
	}
	// v cycles back to dashboard.
	m = drive(m, tea.KeyPressMsg{Text: "v"})
	if m.view != 0 {
		t.Errorf("view = %d, want 0", m.view)
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

func TestRefreshKeepsSelectedIdentityAndClampsSearch(t *testing.T) {
	s := store.New()
	add := func(host string) {
		s.AddSeed(parser.Record{Vhost: host, IP: host, Method: "GET", Path: "/", Status: 200, Time: time.Now()})
	}
	add("alpha")
	add("beta")
	m := New(s, nil, nil)
	m.sorts[0] = store.SortHits
	m.refresh()
	for i, row := range m.hosts {
		if row.Key == "beta" {
			m.sel[0] = i
		}
	}
	add("beta")
	m.refresh()
	if m.hosts[m.sel[0]].Key != "beta" {
		t.Fatal("selection moved to another host after reorder")
	}
	m.focus, m.search = 0, "alpha"
	m.refresh()
	if m.sel[0] != 0 || len(m.filteredRows(0)) != 1 || m.filteredRows(0)[0].Key != "alpha" {
		t.Fatal("selection was not clamped to visible search results")
	}
}

func TestServicesKeepDifferentMeasurementScopes(t *testing.T) {
	m := New(store.New(), nil, func() []fpm.PoolView {
		return []fpm.PoolView{{Pool: fpm.Pool{Name: "www"}, Status: &fpm.Status{SlowRequests: 7}, SlowSeen: 3, RSSKB: 1024,
			Access: fpm.ServiceMetrics{Requests: 2, DurationUs: 4000, DurationCount: 2, MemoryBytes: 4194304, MemoryCount: 2, Bad: 1}}}
	})
	out := strings.Join(m.fpmLines(220, 10), "\n")
	for _, want := range []string{"slow:7", "slowlog:3", "est-mem:", "log:2", "avg:2.0ms", "req-mem:", "bad:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("service measurements missing %q: %s", want, out)
		}
	}
}
