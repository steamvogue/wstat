package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/steamvogue/wstat/internal/fpm"
	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/parser"
	"github.com/steamvogue/wstat/internal/store"
)

func scrollModel(count int) Model {
	s := store.New()
	for i := 0; i < count; i++ {
		s.AddSeed(parser.Record{Vhost: fmt.Sprintf("host-%03d", i), IP: fmt.Sprintf("client-%03d", i), Method: "GET", Path: fmt.Sprintf("/row-%03d", i), Status: 200, Time: time.Unix(int64(i), 0)})
	}
	m := New(s, nil, nil)
	m.width, m.height = 160, 40
	m.refresh()
	return m
}

func TestStreamSearchNavigationUsesVisibleRows(t *testing.T) {
	m := scrollModel(120)
	for i := 0; i < 10; i++ {
		m.st.AddSeed(parser.Record{Vhost: "test", IP: "test", Method: "GET", Path: fmt.Sprintf("/match-%02d", i), Status: 200})
	}
	m.refresh()
	m.focus = 3
	m = drive(m, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Text: "match"}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Text: "G"})
	if m.sel[3] != 9 {
		t.Fatalf("last search result selection=%d, want 9", m.sel[3])
	}
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.sel[3] != 8 {
		t.Fatal("Up did not move to the previous visible search result")
	}
}

func TestStreamScrollKeepsRequestDuringRollingUpdates(t *testing.T) {
	m := scrollModel(120)
	m.focus, m.sel[3] = 3, 20
	// Use a real navigation event to stop following the bottom.
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyUp})
	want := m.stream[m.sel[3]]
	for i := 120; i < 130; i++ {
		m.st.AddSeed(parser.Record{Vhost: "new", IP: "new", Method: "GET", Path: fmt.Sprintf("/row-%03d", i), Status: 200})
	}
	m = drive(m, tickMsg(time.Now()))
	if m.stream[m.sel[3]].Path != want.Path {
		t.Fatalf("scrolled request drifted from %s to %s", want.Path, m.stream[m.sel[3]].Path)
	}
}

func TestStreamResumeReturnsToLatestImmediately(t *testing.T) {
	m := scrollModel(120)
	m.focus = 3
	m = drive(m, tea.KeyPressMsg{Text: "z"}, tea.KeyPressMsg{Text: "g"}, tea.KeyPressMsg{Text: "z"})
	if m.streamPaused || m.sel[3] != len(m.stream)-1 {
		t.Fatal("unfreezing did not resume at the newest request")
	}
}

func TestSearchPreservesOtherPanelPositions(t *testing.T) {
	m := scrollModel(120)
	m.sel, m.focus = [4]int{10, 11, 12, 20}, 1
	m = drive(m, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Text: "/row-11"}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sel[0] != 10 || m.sel[2] != 12 || m.sel[3] != 20 {
		t.Fatal("search moved unrelated panel selections")
	}
	if len(m.filteredRows(0)) != 120 || len(m.filteredRows(2)) != 120 {
		t.Fatal("URL search filtered unrelated tables")
	}
	m = drive(m, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Text: "é"}, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if !utf8.ValidString(m.search) {
		t.Fatal("backspace corrupted a Unicode search")
	}
}

func TestEmptyPanelsNeverHaveNegativeSelection(t *testing.T) {
	m := New(store.New(), nil, nil)
	for panel := 0; panel < 4; panel++ {
		m.focus = panel
		m = drive(m, tea.KeyPressMsg{Text: "G"})
		if m.sel[panel] != 0 {
			t.Errorf("panel %d: empty end selection=%d", panel, m.sel[panel])
		}
	}
	m = drive(m, tea.KeyPressMsg{Text: "v"})
	for _, key := range []string{"1", "2", "3", "4"} {
		m = drive(m, tea.KeyPressMsg{Text: key}, tea.KeyPressMsg{Text: "G"}, tea.KeyPressMsg{Code: tea.KeyDown})
		if m.serviceSel != [2]int{} || m.sel[3] != 0 {
			t.Errorf("empty Services selection became invalid after %s", key)
		}
	}
}

func TestSelectedRowsRemainVisibleAcrossResizeAndZoom(t *testing.T) {
	for panel := 0; panel < 4; panel++ {
		for _, size := range [][2]int{{160, 40}, {100, 24}, {80, 18}} {
			for _, zoom := range []bool{false, true} {
				t.Run(fmt.Sprintf("panel%d/%dx%d/zoom%t", panel, size[0], size[1], zoom), func(t *testing.T) {
					m := scrollModel(120)
					m.focus, m.zoom = panel, zoom
					m = drive(m, tea.KeyPressMsg{Text: "G"}, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					want := m.stream[m.sel[3]].Path
					if panel < 3 {
						row := m.filteredRows(panel)[m.sel[panel]]
						want = row.Key
						if panel == 1 {
							want = row.Path
						}
					}
					out := m.render()
					if lipgloss.Height(out) != m.height || lipgloss.Width(out) > m.width || !strings.Contains(out, want) {
						t.Fatalf("selected %s is hidden or render exceeds terminal bounds", want)
					}
				})
			}
		}
	}
}

func TestServicesNavigationReachesLastPool(t *testing.T) {
	m := scrollModel(120)
	m.fpmViews = func() []fpm.PoolView {
		out := make([]fpm.PoolView, 30)
		for i := range out {
			out[i] = fpm.PoolView{Pool: fpm.Pool{Name: fmt.Sprintf("pool-%02d", i)}, Disabled: true}
		}
		return out
	}
	before := m.sel
	m = drive(m, tea.KeyPressMsg{Text: "v"}, tea.KeyPressMsg{Text: "1"}, tea.KeyPressMsg{Text: "G"})
	if !strings.Contains(m.render(), "pool-29") || m.sel != before {
		t.Fatal("Services navigation cannot reach the last pool or moved hidden dashboard selections")
	}
}

func TestStreamIdenticalRequestsAndExpiry(t *testing.T) {
	m := New(store.New(), nil, nil)
	r := parser.Record{Vhost: "test", IP: "test", Method: "GET", Path: "/same", Status: 200}
	for i := 0; i < 120; i++ {
		m.st.AddSeed(r)
	}
	m.refresh()
	m.focus = 3
	m = drive(m, tea.KeyPressMsg{Text: "z"}, tea.KeyPressMsg{Code: tea.KeyUp})
	id := m.stream[m.sel[3]].StreamID
	for i := 0; i < 20; i++ {
		m.st.AddSeed(r)
	}
	m = drive(m, tickMsg(time.Now()))
	if m.stream[m.sel[3]].StreamID != id {
		t.Fatal("identical requests confused the frozen stream selection")
	}
	for i := 0; i < 100; i++ {
		m.st.AddSeed(r)
	}
	m = drive(m, tickMsg(time.Now()))
	if m.sel[3] != 0 || m.streamFollowing || !m.streamPaused {
		t.Fatal("expired request did not fall back to the oldest retained request")
	}
}

func TestSearchEmptyResultsAndCancel(t *testing.T) {
	for panel := 0; panel < 4; panel++ {
		m := scrollModel(120)
		m.focus = panel
		m = drive(m, tea.KeyPressMsg{Text: "G"}, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Text: "no-such-row"}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Text: "G"}, tea.KeyPressMsg{Code: tea.KeyDown})
		if m.sel[panel] != 0 {
			t.Fatalf("panel %d: empty search did not clamp selection", panel)
		}
		m = drive(m, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.search != "" || m.sel[panel] != 0 {
			t.Fatalf("panel %d: cancel did not restore unsearched rows at the top", panel)
		}
	}
}

func TestTableFilterAndSortScopes(t *testing.T) {
	for _, key := range []string{"x", "b", "t", "X", "m", "s", "h", "c", "p"} {
		for focus := 0; focus < 3; focus++ {
			t.Run(fmt.Sprintf("%s/panel%d", key, focus), func(t *testing.T) {
				m := scrollModel(120)
				m.focus, m.sel = focus, [4]int{10, 11, 12, 30}
				before := m.sel
				m = drive(m, tea.KeyPressMsg{Text: key})
				for panel := 0; panel < 3; panel++ {
					reset := key == "x" || key == "b" || key == "t" || key == "X" || key == "m" && panel == 1 || key == "s" && panel == focus || key == "h" && focus == 0 && panel != 0
					want := before[panel]
					if reset {
						want = 0
					}
					if m.sel[panel] != want {
						t.Fatalf("panel %d selection=%d, want %d", panel, m.sel[panel], want)
					}
				}
			})
		}
	}
}

func TestTableSelectionAndViewportOnReorder(t *testing.T) {
	for panel := 0; panel < 3; panel++ {
		m := scrollModel(120)
		m.focus, m.sel[panel] = panel, 30
		want := m.filteredRows(panel)[30].Key
		for i := 0; i < 10; i++ {
			m.st.AddSeed(parser.Record{Vhost: "host-030", IP: "client-030", Method: "GET", Path: "/row-030", Status: 200})
		}
		m = drive(m, tickMsg(time.Now()))
		if m.filteredRows(panel)[m.sel[panel]].Key != want || m.sel[panel] != 0 {
			t.Fatalf("panel %d lost selected row during reorder", panel)
		}
	}
}

func TestServicesSourcesSearchAndViewIsolation(t *testing.T) {
	root := t.TempDir()
	var sources []logsrc.Source
	for i := 0; i < 30; i++ {
		path := filepath.Join(root, fmt.Sprintf("source-%02d.log", i))
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, logsrc.Source{Path: path, Vhost: fmt.Sprintf("src-%02d", i)})
	}
	tailer := logsrc.Start(sources, nil, 0, nil)
	defer tailer.Stop()
	m := scrollModel(120)
	m.tailer = tailer
	before := m.sel
	m = drive(m, tea.KeyPressMsg{Text: "v"}, tea.KeyPressMsg{Text: "2"}, tea.KeyPressMsg{Text: "G"})
	if !strings.Contains(m.render(), "src-29") || m.sel != before {
		t.Fatalf("source scrolling hid the last source or moved dashboard selections: sources=%d selection=%v dashboard=%v", len(m.tailer.Sources()), m.serviceSel, m.sel)
	}
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.WindowSizeMsg{Width: 80, Height: 18})
	if !strings.Contains(m.render(), "src-29") {
		t.Fatal("source zoom or resize lost selection")
	}
	m = drive(m, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Text: "source-29"}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Text: "G"})
	if m.serviceSel[1] != 0 || !strings.Contains(m.render(), "src-29") || m.sel != before {
		t.Fatal("source search did not use its visible rows")
	}
	m = drive(m, tea.KeyPressMsg{Text: "h"}, tea.KeyPressMsg{Text: "c"}, tea.KeyPressMsg{Text: "p"}, tea.KeyPressMsg{Text: "s"}, tea.KeyPressMsg{Text: "v"})
	if m.filters.Any() || m.sel != before || m.sorts != [3]store.SortKey{} {
		t.Fatal("Services actions changed hidden dashboard state")
	}
}

func TestServicesSelectionSurvivesPoolChanges(t *testing.T) {
	m := scrollModel(120)
	views := []fpm.PoolView{{Pool: fpm.Pool{Name: "alpha"}}, {Pool: fpm.Pool{Name: "beta"}}, {Pool: fpm.Pool{Name: "gamma"}}}
	m.fpmViews = func() []fpm.PoolView { return views }
	m = drive(m, tea.KeyPressMsg{Text: "v"}, tea.KeyPressMsg{Code: tea.KeyDown})
	views = append([]fpm.PoolView{{Pool: fpm.Pool{Name: "new"}}}, views...)
	m = drive(m, tickMsg(time.Now()))
	if m.serviceSel[0] != 2 {
		t.Fatal("new pool displaced the selected pool")
	}
	views = views[:1]
	m = drive(m, tickMsg(time.Now()))
	if m.serviceSel[0] != 0 {
		t.Fatal("removed pool left an invalid selection")
	}
}

func TestViewFocusAndZoomStayIndependent(t *testing.T) {
	m := scrollModel(120)
	m.focus = 1
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Text: "v"}, tea.KeyPressMsg{Text: "2"}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.zoom || !m.serviceZoom || m.focus != 1 || m.serviceFocus != 1 {
		t.Fatal("Services changed dashboard focus or zoom")
	}
	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEscape}, tea.KeyPressMsg{Code: tea.KeyTab}, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, tea.KeyPressMsg{Text: "v"})
	if !m.zoom || m.serviceZoom || m.focus != 1 || m.serviceFocus != 1 {
		t.Fatal("switching views lost independent navigation state")
	}
}
