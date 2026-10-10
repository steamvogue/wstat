package ui

import (
	"fmt"
	"os"
	"path/filepath"
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

func addFreezeTraffic(m *Model, count int) {
	for i := 0; i < count; i++ {
		m.st.Add(parser.Record{Vhost: "new-host", IP: "new-client", Method: "POST", Path: fmt.Sprintf("/new-%03d", i), Status: 500, Time: time.Now()})
	}
}

func TestEachTableFreezeRetainsValuesOrderAndSelection(t *testing.T) {
	for panel := 0; panel < 3; panel++ {
		t.Run(fmt.Sprint(panel), func(t *testing.T) {
			m := scrollModel(120)
			m.focus, m.sel[panel] = panel, 30
			before := append([]store.Row(nil), m.rawRows(panel)...)
			m = drive(m, tea.KeyPressMsg{Text: "f"})
			addFreezeTraffic(&m, 20)
			m = drive(m, tickMsg(time.Now()))
			if !reflect.DeepEqual(m.rawRows(panel), before) || m.sel[panel] != 30 || m.tot.Reqs != 140 {
				t.Fatal("frozen rows/selection changed or ingestion stopped")
			}
			for other := 0; other < 3; other++ {
				if other != panel && len(m.rawRows(other)) <= 120 {
					t.Fatalf("unfrozen pane %d stopped updating", other)
				}
			}
			m = drive(m, tea.KeyPressMsg{Code: tea.KeyDown}, tickMsg(time.Now()))
			if m.sel[panel] != 31 || !reflect.DeepEqual(m.rawRows(panel), before) {
				t.Fatal("scrolling or ticking replaced the frozen data")
			}
			selected := before[31].Key
			m = drive(m, tea.KeyPressMsg{Text: "f"})
			if len(m.rawRows(panel)) <= 120 || m.rawRows(panel)[m.sel[panel]].Key != selected {
				t.Fatal("individual resume did not catch up or retain the inspected row")
			}
		})
	}
}

func TestMultipleFrozenPanesAndGlobalResume(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Text: "F", Code: 'F'},
		{Code: 'F'},
		{Code: 'f', ShiftedCode: 'F', Mod: tea.ModShift},
	} {
		t.Run(key.Keystroke(), func(t *testing.T) {
			m := scrollModel(120)
			m.fpmViews = func() []fpm.PoolView { return []fpm.PoolView{{Pool: fpm.Pool{Name: "alpha"}}} }
			beforeHosts, beforeURLs, beforeClients, beforeStream := m.hosts, m.urls, m.clients, m.stream
			for _, text := range []string{"f", "2", "f", "3", "f", "4", "f", "v", "1", "f", "2", "f", "v", "z"} {
				m = drive(m, tea.KeyPressMsg{Text: text})
			}
			if m.panelFrozen != [panelCount]bool{true, true, true, true, true, true} {
				t.Fatal("freezing other panes cleared an existing freeze")
			}
			addFreezeTraffic(&m, 600) // exceed both the visible snapshot and store ring
			m = drive(m, tickMsg(time.Now()))
			if !reflect.DeepEqual(m.hosts, beforeHosts) || !reflect.DeepEqual(m.urls, beforeURLs) ||
				!reflect.DeepEqual(m.clients, beforeClients) || !reflect.DeepEqual(m.stream, beforeStream) {
				t.Fatal("one of the frozen dashboard snapshots changed")
			}
			m = drive(m, key)
			if m.panelFrozen != [panelCount]bool{} || m.tot.Reqs != 720 || reflect.DeepEqual(m.stream, beforeStream) ||
				!m.streamPaused || m.streamFollowing || m.frozenPools != nil || m.frozenSources != nil || m.frozenErrors != nil {
				t.Fatal("Shift+F did not catch up all panes independently of z pause")
			}
			m = drive(m, tea.KeyPressMsg{Text: "v"})
			if strings.Contains(m.render(), "[frozen]") {
				t.Fatal("hidden Services panes remained frozen")
			}
			m = drive(m, key)
			if m.panelFrozen != [panelCount]bool{} {
				t.Fatal("repeated global resume froze a pane")
			}
		})
	}
}

func TestIndividualResumeLeavesOtherSnapshotsFrozen(t *testing.T) {
	m := scrollModel(120)
	for _, text := range []string{"f", "2", "f", "3", "f", "4", "f", "v", "1", "f", "2", "f", "v", "2"} {
		m = drive(m, tea.KeyPressMsg{Text: text})
	}
	beforeHosts, beforeClients, beforeStream := m.hosts, m.clients, m.stream
	addFreezeTraffic(&m, 20)
	m = drive(m, tea.KeyPressMsg{Text: "f"}, tickMsg(time.Now()))
	if m.panelFrozen != [panelCount]bool{true, false, true, true, true, true} || len(m.urls) != 140 ||
		!reflect.DeepEqual(m.hosts, beforeHosts) || !reflect.DeepEqual(m.clients, beforeClients) || !reflect.DeepEqual(m.stream, beforeStream) {
		t.Fatal("resuming URLs changed another pane's state or snapshot")
	}
}

func TestFrozenStreamScrollingSearchAndSharedView(t *testing.T) {
	m := scrollModel(120)
	m.focus = streamPanel
	m = drive(m, tea.KeyPressMsg{Text: "f"})
	before := append([]parser.Record(nil), m.stream...)
	addFreezeTraffic(&m, 600)
	// Leave auto-follow enabled: ticks must still keep the frozen viewport.
	m = drive(m, tickMsg(time.Now()))
	if !reflect.DeepEqual(m.stream, before) || m.sel[3] != len(before)-1 {
		t.Fatal("auto-follow replaced or moved a frozen stream")
	}
	m = drive(m, tea.KeyPressMsg{Text: "g"}, tea.KeyPressMsg{Code: tea.KeyDown}, tickMsg(time.Now()))
	if m.sel[3] != 1 || !reflect.DeepEqual(m.stream, before) {
		t.Fatal("scrolling refreshed a frozen stream")
	}
	m = drive(m, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Text: "row-119"}, tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.KeyPressMsg{Text: "v"}, tea.KeyPressMsg{Text: "3"}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.WindowSizeMsg{Width: 100, Height: 18}, tickMsg(time.Now()))
	if !m.panelFrozen[streamPanel] || len(m.streamRows()) == 0 || len(m.streamRows()) >= len(before) || m.streamRows()[len(m.streamRows())-1].Path != "/row-119" ||
		!strings.Contains(m.render(), "[frozen]") || lipgloss.Height(m.render()) != 18 || lipgloss.Width(m.render()) > 100 {
		t.Fatal("view switch/search/zoom/resize lost the shared frozen stream")
	}
	m = drive(m, tea.KeyPressMsg{Text: "/"}, tea.KeyPressMsg{Code: tea.KeyEscape}, tea.KeyPressMsg{Text: "G"}, tea.KeyPressMsg{Text: "f"})
	if m.panelFrozen[streamPanel] || reflect.DeepEqual(m.stream, before) || m.sel[3] != len(m.stream)-1 || !strings.HasPrefix(m.stream[m.sel[3]].Path, "/new-") {
		t.Fatal("resuming auto-follow did not jump to the latest live request")
	}
}

func TestFrozenStreamResumeKeepsInspectedRequest(t *testing.T) {
	m := scrollModel(120)
	m.focus = streamPanel
	m = drive(m, tea.KeyPressMsg{Text: "f"}, tea.KeyPressMsg{Code: tea.KeyUp})
	want := m.stream[m.sel[3]].StreamID
	addFreezeTraffic(&m, 10)
	m = drive(m, tickMsg(time.Now()), tea.KeyPressMsg{Text: "f"})
	if m.streamFollowing || m.stream[m.sel[3]].StreamID != want {
		t.Fatal("individual stream resume lost the inspected request")
	}
}

func TestFrozenFilterMarkersAndSortDescribeRetainedSnapshot(t *testing.T) {
	m := scrollModel(120)
	m.filters.Hosts = map[string]bool{"host-000": true}
	m.filters.Paths = map[string]bool{"/row-000": true}
	m.filters.Clients = map[string]bool{"client-000": true}
	m.refresh()
	for _, text := range []string{"f", "2", "f", "3", "f"} {
		m = drive(m, tea.KeyPressMsg{Text: text})
	}
	beforeTitles := [3]string{m.panelTitle(0), m.panelTitle(1), m.panelTitle(2)}
	// All three toggles mutate their existing map; captures must not alias it.
	for _, text := range []string{"1", "h", "s", "2", "p", "s", "3", "c", "s", "b"} {
		m = drive(m, tea.KeyPressMsg{Text: text})
	}
	for panel := 0; panel < 3; panel++ {
		if m.panelTitle(panel) != beforeTitles[panel] || m.panelSort(panel) != store.SortRate {
			t.Fatalf("frozen pane %d displayed pending settings", panel)
		}
	}
	if !m.panelFilters(0).Hosts["host-000"] || !m.panelFilters(1).Paths["/row-000"] || !m.panelFilters(2).Clients["client-000"] {
		t.Fatal("mutating live filters changed frozen markers")
	}
	m = drive(m, tea.KeyPressMsg{Text: "F"})
	if m.panelSort(0) != store.SortHits || m.panelFilters(0).Hosts["host-000"] {
		t.Fatal("global resume did not apply pending settings")
	}
}

func TestFrozenPoolsRetainMetricsWhileLiveAlertsUpdate(t *testing.T) {
	status := &fpm.Status{ActiveProcesses: 1, TotalProcesses: 3}
	views := []fpm.PoolView{{Pool: fpm.Pool{Name: "alpha"}, Status: status}, {Pool: fpm.Pool{Name: "beta"}, Workers: 2}}
	m := scrollModel(120)
	m.fpmViews = func() []fpm.PoolView { return views }
	m = drive(m, tea.KeyPressMsg{Text: "v"}, tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Text: "f"})
	before := m.serviceEntries(0, 160)
	status.ListenQueue = 5
	views[1].Workers = 20
	views = append(views, fpm.PoolView{Pool: fpm.Pool{Name: "gamma"}})
	m = drive(m, tickMsg(time.Now()))
	if !reflect.DeepEqual(m.serviceEntries(0, 160), before) || m.serviceSel[0] != 1 || !strings.Contains(m.fpmPanel(160, 18), "[frozen]") {
		t.Fatal("frozen pool rows/metrics/selection changed")
	}
	if !strings.Contains(strings.Join(m.chips(), " "), "queue") {
		t.Fatal("freezing pool display suppressed a live queue alert")
	}
	m = drive(m, tea.KeyPressMsg{Text: "f"})
	if len(m.fpmSnapshot()) != 3 || m.fpmSnapshot()[0].Status.ListenQueue != 5 || m.fpmSnapshot()[1].Workers != 20 || m.serviceSel[0] != 1 {
		t.Fatal("pool resume did not catch up and preserve selection")
	}
}

func TestFrozenSourcesRetainDiscoveredSourcesUntilResume(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.log")
	if err := os.WriteFile(first, nil, 0600); err != nil {
		t.Fatal(err)
	}
	tailer := logsrc.Start([]logsrc.Source{{Path: first, Vhost: "first"}}, []string{filepath.Join(root, "*.log")}, 0, nil, logsrc.WithRescanEvery(10*time.Millisecond))
	defer tailer.Stop()
	m := scrollModel(120)
	m.tailer = tailer
	m = drive(m, tea.KeyPressMsg{Text: "v"}, tea.KeyPressMsg{Text: "2"}, tea.KeyPressMsg{Text: "G"}, tea.KeyPressMsg{Text: "f"})
	before := m.serviceEntries(1, 160)
	second := filepath.Join(root, "second.log")
	if err := os.WriteFile(second, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(tailer.Sources()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("tailer did not discover the second source")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m = drive(m, tickMsg(time.Now()))
	if !reflect.DeepEqual(m.serviceEntries(1, 160), before) || m.serviceSel[1] != 1 || !strings.Contains(m.sourcesPanel(160, 18), "[frozen]") || !strings.Contains(m.footer(), "live:2") {
		t.Fatal("frozen source rows changed or footer stopped tracking live sources")
	}
	m = drive(m, tea.KeyPressMsg{Text: "f"})
	if len(m.serviceEntries(1, 160)) != 3 || m.serviceSel[1] != 1 {
		t.Fatal("source resume did not catch up and retain the selected source")
	}
}

func TestFreezeEmptyPanesThenResume(t *testing.T) {
	m := New(store.New(), nil, nil)
	m.width, m.height = 160, 40
	m.refresh()
	for _, text := range []string{"f", "2", "f", "3", "f", "4", "f", "v", "1", "f"} {
		m = drive(m, tea.KeyPressMsg{Text: text})
	}
	addFreezeTraffic(&m, 2)
	m.fpmViews = func() []fpm.PoolView { return []fpm.PoolView{{Pool: fpm.Pool{Name: "new"}}} }
	m = drive(m, tickMsg(time.Now()))
	if len(m.hosts)+len(m.urls)+len(m.clients)+len(m.stream)+len(m.fpmSnapshot()) != 0 {
		t.Fatal("an empty frozen pane adopted newly arrived data")
	}
	m = drive(m, tea.KeyPressMsg{Text: "F"})
	if len(m.hosts) != 1 || len(m.urls) != 2 || len(m.clients) != 1 || len(m.stream) != 2 || len(m.fpmSnapshot()) != 1 {
		t.Fatal("global resume did not populate previously empty panes")
	}
}
