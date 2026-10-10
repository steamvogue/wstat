// Package ui renders the wstat dashboard: header, HOSTS/URLS/CLIENTS
// panels, live request stream, filters and colors.
package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/steamvogue/wstat/internal/fpm"
	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/parser"
	"github.com/steamvogue/wstat/internal/store"
)

const refreshInterval = 500 * time.Millisecond

type tickMsg time.Time

type theme struct {
	accent, borderDim, label, dim, faint, chip, white string
	status2, status3, status4, status5                string
	palette                                           []string
}

var themes = []theme{
	{ // amber (default)
		accent: "#e8a33d", borderDim: "#4a4a5a", label: "#8a8a9a", dim: "#6a6a7a",
		faint: "#454555", chip: "#e8a33d", white: "#ffffff",
		status2: "#41c98e", status3: "#40c4ff", status4: "#f2c14e", status5: "#f05a5a",
		palette: []string{"#e8a33d", "#41c98e", "#40c4ff", "#c792ea", "#f78c6c",
			"#89ddff", "#ff5370", "#a9dc76", "#bb80b3", "#aed581"},
	},
	{ // ocean
		accent: "#40c4ff", borderDim: "#3a4a5a", label: "#8aa0b4", dim: "#68808f",
		faint: "#45555f", chip: "#40c4ff", white: "#e8f4ff",
		status2: "#64e6a0", status3: "#7fd8ff", status4: "#ffd166", status5: "#ff7b7b",
		palette: []string{"#40c4ff", "#64e6a0", "#e0aaff", "#ffd166", "#7fd8ff",
			"#f49cbb", "#90f1ef", "#c0f0c0", "#bfa5ff", "#ffe0a3"},
	},
	{ // mono
		accent: "#c8c8d8", borderDim: "#4a4a55", label: "#9a9aa8", dim: "#787885",
		faint: "#55555f", chip: "#c8c8d8", white: "#ffffff",
		status2: "#b0b0c0", status3: "#a0a0b5", status4: "#c8b890", status5: "#e8b8b8",
		palette: []string{"#c8c8d8", "#b8b8c8", "#a8a8b8", "#9898a8", "#d8d8e8",
			"#888898", "#e0e0f0", "#909098", "#b0b0b8", "#a0a0a8"},
	},
}

var (
	styBorderFocus lipgloss.Style
	styBorderDim   lipgloss.Style
	styTitle       lipgloss.Style
	styTitleDim    lipgloss.Style
	styLogo        lipgloss.Style
	styLabel       lipgloss.Style
	styDim         lipgloss.Style
	styFaint       lipgloss.Style
	styAccent      lipgloss.Style
	styChip        lipgloss.Style
	styStatusOK    lipgloss.Style
	styStatus3xx   lipgloss.Style
	styStatus4xx   lipgloss.Style
	styStatus5xx   lipgloss.Style
	stySel         lipgloss.Style
	styStreamTime  lipgloss.Style
	hostPalette    []lipgloss.Style
)

func applyTheme(i int) {
	th := themes[i%len(themes)]
	styBorderFocus = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(th.accent))
	styBorderDim = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(th.borderDim))
	styTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(th.accent))
	styTitleDim = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(th.dim))
	styLogo = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(th.accent))
	styLabel = lipgloss.NewStyle().Foreground(lipgloss.Color(th.label))
	styDim = lipgloss.NewStyle().Foreground(lipgloss.Color(th.dim))
	styFaint = lipgloss.NewStyle().Foreground(lipgloss.Color(th.faint))
	styAccent = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(th.accent))
	styChip = lipgloss.NewStyle().Foreground(lipgloss.Color("#0d0d12")).Background(lipgloss.Color(th.chip))
	styStatusOK = lipgloss.NewStyle().Foreground(lipgloss.Color(th.status2))
	styStatus3xx = lipgloss.NewStyle().Foreground(lipgloss.Color(th.status3))
	styStatus4xx = lipgloss.NewStyle().Foreground(lipgloss.Color(th.status4))
	styStatus5xx = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(th.status5))
	stySel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(th.white)).Background(lipgloss.Color("#3a3a4a"))
	styStreamTime = lipgloss.NewStyle().Foreground(lipgloss.Color(th.label))
	hostPalette = make([]lipgloss.Style, len(th.palette))
	for i, c := range th.palette {
		hostPalette[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
}

func init() { applyTheme(0) }

func vhostStyle(v string) lipgloss.Style {
	var h uint32 = 2166136261
	for i := 0; i < len(v); i++ {
		h = (h ^ uint32(v[i])) * 16777619
	}
	return hostPalette[h%uint32(len(hostPalette))]
}

func statusStyle(status int) lipgloss.Style {
	switch status / 100 {
	case 2:
		return styStatusOK
	case 3:
		return styStatus3xx
	case 4:
		return styStatus4xx
	case 5:
		return styStatus5xx
	default:
		return styDim
	}
}

// Model is the bubbletea application state.
type Model struct {
	st              *store.Store
	tailer          *logsrc.Tailer
	width           int
	height          int
	focus           int // 0 hosts, 1 urls, 2 clients, 3 stream
	view            int // 0 dashboard, 1 services
	zoom            bool
	sel             [4]int
	filters         store.Filters
	sorts           [3]store.SortKey
	frozen          bool
	hostsFrozen     bool
	theme           int
	search          string
	searchMode      bool
	searchPanel     int
	searchServices  bool
	streamFollowing bool
	serviceFocus    int // 0 pools, 1 sources, 2 stream
	serviceZoom     bool
	serviceSel      [2]int
	serviceKeys     [2][]string

	fpmViews func() []fpm.PoolView

	hosts   []store.Row
	urls    []store.Row
	clients []store.Row
	stream  []parser.Record
	tot     store.Totals
	bad     int64
	started time.Time
}

func New(st *store.Store, tailer *logsrc.Tailer, fpmViews func() []fpm.PoolView) Model {
	return Model{st: st, tailer: tailer, fpmViews: fpmViews, started: time.Now(), streamFollowing: true}
}

func (m Model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) refresh(resetPanels ...int) {
	// Keep the stream pinned to the bottom unless frozen or scrolled up.
	atBottom := !m.frozen && m.streamFollowing
	oldStream := m.streamRows()
	var streamID uint64
	if i := m.sel[3]; i >= 0 && i < len(oldStream) {
		streamID = oldStream[i].StreamID
	}

	var selected [3]string
	var reset [3]bool
	for _, panel := range resetPanels {
		reset[panel] = true
	}
	for panel := 0; panel < 3; panel++ {
		rows := m.filteredRows(panel)
		// Row zero follows the live leaders. Follow a row's identity only when
		// the user is inspecting farther down, within the same filter/sort.
		if i := m.sel[panel]; !reset[panel] && i > 0 && i < len(rows) {
			selected[panel] = rows[i].Key
		}
	}

	hosts, urls, clients, stream, tot, bad := m.st.Snapshot(m.filters, m.sorts, 200)
	// Snapshot rows are immutable, so retaining this slice freezes both order
	// and values without copying or pausing ingestion into the store.
	if !m.hostsFrozen {
		m.hosts = hosts
	}
	m.urls, m.clients, m.stream, m.tot, m.bad = urls, clients, stream, tot, bad

	for panel, key := range selected {
		if reset[panel] || key != "" {
			m.sel[panel] = 0
		}
		if key == "" {
			continue
		}
		for i, r := range m.filteredRows(panel) {
			if r.Key == key {
				m.sel[panel] = i
				break
			}
		}
	}
	streamRows := m.streamRows()
	if atBottom {
		m.sel[3] = max(0, len(streamRows)-1)
	} else {
		m.sel[3] = 0
		for i, r := range streamRows {
			if streamID != 0 && r.StreamID == streamID {
				m.sel[3] = i
				break
			}
		}
	}
	if m.view == 1 {
		m.refreshServices()
	}
	m.clampSel()
}

func (m *Model) clampSel() {
	m.sel[0] = clamp(m.sel[0], 0, len(m.filteredRows(0))-1)
	m.sel[1] = clamp(m.sel[1], 0, len(m.filteredRows(1))-1)
	m.sel[2] = clamp(m.sel[2], 0, len(m.filteredRows(2))-1)
	m.sel[3] = clamp(m.sel[3], 0, len(m.streamRows())-1)
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.refresh()
		return m, tick()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.searchMode {
			m.searchKey(msg)
			return m, nil
		}
		if cmd, handled := m.navKey(msg); handled {
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) searchKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "esc":
		m.searchMode = false
		m.search = ""
	case "enter":
		m.searchMode = false
	case "backspace":
		if n := len(m.search); n > 0 {
			_, size := utf8.DecodeLastRuneInString(m.search)
			m.search = m.search[:n-size]
		}
	default:
		if msg.Text != "" {
			m.search += msg.Text
		}
	}
	if m.searchServices {
		m.serviceSel[m.searchPanel] = 0
		m.refreshServices()
	} else {
		m.sel[m.searchPanel] = 0
		if m.searchPanel == 3 {
			m.streamFollowing = false
		}
	}
	m.clampSel()
}

func toggle(set map[string]bool, v string) map[string]bool {
	if set == nil {
		set = map[string]bool{}
	}
	if set[v] {
		delete(set, v)
	} else {
		set[v] = true
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func (m *Model) navKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c", "q":
		return tea.Quit, true
	case "tab":
		if m.view == 1 {
			m.serviceFocus = (m.serviceFocus + 1) % 3
			return nil, true
		}
		m.focus = (m.focus + 1) % 4
		return nil, true
	case "shift+tab":
		if m.view == 1 {
			m.serviceFocus = (m.serviceFocus + 2) % 3
			return nil, true
		}
		m.focus = (m.focus + 3) % 4
		return nil, true
	case "enter":
		if m.view == 1 {
			m.serviceZoom = !m.serviceZoom
		} else {
			m.zoom = !m.zoom
		}
		return nil, true
	case "esc":
		if m.view == 1 {
			m.serviceZoom = false
		} else {
			m.zoom = false
		}
		return nil, true
	case "1", "2", "3", "4":
		if m.view == 1 {
			m.serviceFocus = min(int(msg.String()[0]-'1'), 2)
			return nil, true
		}
		m.focus = int(msg.String()[0] - '1')
		return nil, true
	case "j", "down":
		m.moveSelection(1)
	case "k", "up":
		m.moveSelection(-1)
	case "g":
		m.jumpSelection(false)
	case "G":
		m.jumpSelection(true)
	case "/":
		panel, services := m.focus, false
		if m.view == 1 {
			panel, services = m.serviceFocus, m.serviceFocus != 2
			if !services {
				panel = 3
			}
		}
		if panel != m.searchPanel || services != m.searchServices {
			m.search = ""
		}
		m.searchPanel, m.searchServices = panel, services
		m.searchMode = true
		return nil, true
	case "h": // toggle host filter from selected hosts row
		if m.view == 0 && m.focus == 0 {
			if rows := m.filteredRows(0); len(rows) > 0 {
				i := clamp(m.sel[0], 0, len(rows)-1)
				m.filters.Hosts = toggle(m.filters.Hosts, rows[i].Key)
				m.refreshFilters(1, 2)
			}
		}
	case "c": // toggle client filter from selected clients row
		if m.view == 0 && m.focus == 2 {
			if rows := m.filteredRows(2); len(rows) > 0 {
				i := clamp(m.sel[2], 0, len(rows)-1)
				m.filters.Clients = toggle(m.filters.Clients, rows[i].Key)
				m.refreshFilters()
			}
		}
	case "p": // toggle path filter from selected urls row
		if m.view == 0 && m.focus == 1 {
			if rows := m.filteredRows(1); len(rows) > 0 {
				i := clamp(m.sel[1], 0, len(rows)-1)
				m.filters.Paths = toggle(m.filters.Paths, rows[i].Path)
				m.refreshFilters()
			}
		}
	case "x": // cycle status filter
		m.hostsFrozen = false
		switch m.filters.Mask {
		case 0:
			m.filters.Mask = store.MaskErr
		case store.MaskErr:
			m.filters.Mask = store.MaskOK
		default:
			m.filters.Mask = 0
		}
		m.refreshFilters(0, 1, 2)
	case "m": // cycle method filter
		switch m.filters.Method {
		case "":
			m.filters.Method = "GET"
		case "GET":
			m.filters.Method = "POST"
		case "POST":
			m.filters.Method = "HEAD"
		default:
			m.filters.Method = ""
		}
		m.refreshFilters(1)
	case "b": // cycle bots filter
		m.hostsFrozen = false
		switch m.filters.Bots {
		case 0:
			m.filters.Bots = +1
		case +1:
			m.filters.Bots = -1
		default:
			m.filters.Bots = 0
		}
		m.refreshFilters(0, 1, 2)
	case "t": // cycle static-asset filter
		m.hostsFrozen = false
		switch m.filters.Static {
		case 0:
			m.filters.Static = -1
		default:
			m.filters.Static = 0
		}
		m.refreshFilters(0, 1, 2)
	case "s": // cycle sort of focused table panel
		if m.view == 0 && m.focus < 3 {
			m.sorts[m.focus] = (m.sorts[m.focus] + 1) % 4
			if m.focus == 0 {
				m.hostsFrozen = false
			}
			m.refresh(m.focus)
		}
	case "F": // freeze/unfreeze the hosts panel independently of the stream
		m.hostsFrozen = !m.hostsFrozen
		if !m.hostsFrozen {
			m.refresh()
		}
		return nil, true
	case "f": // freeze stream auto-follow
		m.frozen = !m.frozen
		m.streamFollowing = !m.frozen
		if !m.frozen {
			m.sel[3] = max(0, len(m.streamRows())-1)
		}
		return nil, true
	case "v": // cycle Dashboard / Services
		m.view = (m.view + 1) % 2
		if m.view == 1 {
			m.refreshServices()
		}
		return nil, true
	case "T": // cycle theme
		m.theme = (m.theme + 1) % len(themes)
		applyTheme(m.theme)
		return nil, true
	case "X": // clear every filter
		m.hostsFrozen = false
		m.filters = store.Filters{}
		m.search = ""
		m.sel = [4]int{}
		m.serviceSel = [2]int{}
		m.refreshFilters(0, 1, 2)
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) refreshFilters(resetPanels ...int) {
	m.streamFollowing = !m.frozen
	m.refresh(resetPanels...)
}

func (m Model) streamFocused() bool {
	return m.view == 0 && m.focus == 3 || m.view == 1 && m.serviceFocus == 2
}

func (m *Model) navigationSelection() (*int, int) {
	if m.view == 1 && m.serviceFocus < 2 {
		return &m.serviceSel[m.serviceFocus], len(m.serviceEntries(m.serviceFocus, 0))
	}
	if m.streamFocused() {
		return &m.sel[3], len(m.streamRows())
	}
	return &m.sel[m.focus], len(m.filteredRows(m.focus))
}

func (m *Model) moveSelection(delta int) {
	sel, count := m.navigationSelection()
	*sel = clamp(*sel+delta, 0, count-1)
	if m.streamFocused() {
		m.streamFollowing = !m.frozen && delta > 0 && *sel == max(0, count-1)
	}
}

func (m *Model) jumpSelection(end bool) {
	sel, count := m.navigationSelection()
	*sel = 0
	if end {
		*sel = max(0, count-1)
	}
	if m.streamFocused() {
		m.streamFollowing = end && !m.frozen
	}
}

func (m Model) searchQuery(panel int, services bool) string {
	if m.searchPanel == panel && m.searchServices == services {
		return m.search
	}
	return ""
}

func (m Model) streamRows() []parser.Record {
	query := m.searchQuery(3, false)
	if query == "" {
		return m.stream
	}
	var rows []parser.Record
	for _, r := range m.stream {
		if fuzzy(query, r.Vhost+" "+r.Path+" "+r.IP+" "+r.Method) {
			rows = append(rows, r)
		}
	}
	return rows
}

func (m *Model) filteredRows(panel int) []store.Row {
	query := m.searchQuery(panel, false)
	if query == "" {
		return m.rawRows(panel)
	}
	var out []store.Row
	for _, r := range m.rawRows(panel) {
		if fuzzy(query, r.Key+" "+r.Path+" "+r.UA) {
			out = append(out, r)
		}
	}
	return out
}

func (m *Model) rawRows(panel int) []store.Row {
	switch panel {
	case 0:
		return m.hosts
	case 1:
		return m.urls
	case 2:
		return m.clients
	}
	return nil
}

// fuzzy reports whether needle is a case-insensitive subsequence of hay.
func fuzzy(needle, hay string) bool {
	ni := 0
	for i := 0; i < len(hay) && ni < len(needle); i++ {
		c, n := hay[i], needle[ni]
		if 'A' <= c && c <= 'Z' {
			c += 32
		}
		if 'A' <= n && n <= 'Z' {
			n += 32
		}
		if c == n {
			ni++
		}
	}
	return ni == len(needle)
}

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m Model) render() string {
	if m.width < 40 || m.height < 12 {
		return "wstat: terminal too small (need >= 40x12)\n"
	}
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteByte('\n')
	// Reserve one row each for the header and help, including when panels or
	// filter chips would otherwise overflow a small terminal.
	b.WriteString(lipgloss.NewStyle().Height(m.height - 2).MaxHeight(m.height - 2).MaxWidth(m.width).Render(m.body()))
	b.WriteByte('\n')
	if m.searchMode {
		b.WriteString(lipgloss.NewStyle().MaxWidth(m.width).Render(m.searchLine()))
	} else {
		b.WriteString(m.footer())
	}
	return b.String()
}

func (m Model) header() string {
	var b strings.Builder
	b.WriteString(styLogo.Render("wstat"))
	b.WriteString(styDim.Render(" global"))
	b.WriteByte(' ')
	if m.tot.Reqs > 0 {
		b.WriteString(styLabel.Render(fmt.Sprintf("%d tracked hosts", m.tot.TrackedHosts)))
		b.WriteString(styDim.Render(" │ "))
		b.WriteString(styAccent.Render(humanRate(m.tot.Rate)))
		b.WriteString(styLabel.Render(" req/s"))
		b.WriteString(styDim.Render(" │ "))
		b.WriteString(styLabel.Render(humanBytes(int64(m.tot.ByteRate)) + "/s"))
	}
	for i := 0; i < 4; i++ {
		if n := m.tot.Class[i]; n > 0 {
			b.WriteString(styDim.Render(" │ "))
			s := statusStyle((i + 2) * 100)
			b.WriteString(s.Render(fmt.Sprintf("%dxx %s", i+2, humanInt(n))))
		}
	}
	for _, chip := range m.chips() {
		b.WriteByte(' ')
		b.WriteString(styChip.Render(" " + chip + " "))
	}
	clock := styLabel.Render(time.Now().Format("15:04:05"))
	leftW := m.width - lipgloss.Width(clock)
	left := lipgloss.NewStyle().MaxWidth(leftW).Render(b.String())
	return left + strings.Repeat(" ", max(0, leftW-lipgloss.Width(left))) + clock
}

func (m Model) chips() []string {
	var chips []string
	if names := m.filters.Hosts; len(names) > 0 {
		chips = append(chips, "host:"+strings.Join(sortedKeys(names), ","))
	}
	if names := m.filters.Clients; len(names) > 0 {
		chips = append(chips, "ip:"+strings.Join(sortedKeys(names), ","))
	}
	if names := m.filters.Paths; len(names) > 0 {
		chips = append(chips, "path:"+strconvLen(names))
	}
	switch m.filters.Mask {
	case store.MaskErr:
		chips = append(chips, "4xx-5xx")
	case store.MaskOK:
		chips = append(chips, "2xx-3xx")
	}
	if m.filters.Method != "" {
		chips = append(chips, m.filters.Method)
	}
	switch m.filters.Bots {
	case +1:
		chips = append(chips, "bots")
	case -1:
		chips = append(chips, "humans")
	}
	if m.filters.Static == -1 {
		chips = append(chips, "no-static")
	}
	if m.search != "" {
		chips = append(chips, "/"+m.search)
	}
	if m.frozen {
		chips = append(chips, "frozen")
	}
	if m.tot.HostEvicted+m.tot.URLEvicted+m.tot.ClientEvicted+m.tot.AssociationEvicted > 0 {
		chips = append(chips, "partial detail: evicted")
	}
	if m.filters.Method != "" {
		chips = append(chips, "method: URLs+stream")
	}
	if len(m.filters.Paths) > 0 || len(m.filters.Clients) > 0 {
		chips = append(chips, "path/ip: stream")
	}
	if views := m.fpmSnapshot(); len(views) > 0 {
		if ok, msg := fpm.AnyAlert(views); ok {
			chips = append(chips, msg)
		}
	}
	return chips
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	// tiny insertion sort; filter sets are small
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func strconvLen(set map[string]bool) string {
	if len(set) == 1 {
		for k := range set {
			return k
		}
	}
	return fmt.Sprintf("%d", len(set))
}

func (m Model) footer() string {
	live, replay := 0, 0
	var sources []logsrc.Source
	if m.tailer != nil {
		sources = m.tailer.Sources()
	}
	for _, s := range sources {
		if s.Replay {
			replay++
		} else {
			live++
		}
	}
	src := fmt.Sprintf("live:%d", live)
	if replay > 0 {
		src += fmt.Sprintf(" replay:%d", replay)
	}
	right := styLabel.Render(src)
	if m.bad > 0 {
		right += styDim.Render(" · ") + styStatus4xx.Render(fmt.Sprintf("bad:%d", m.bad))
	}
	elapsed := time.Since(m.started).Round(time.Second)
	right += styDim.Render(fmt.Sprintf(" · %s", elapsed))
	leftW := m.width - lipgloss.Width(right) - 2
	// On narrow terminals, help takes priority over source statistics.
	if leftW < lipgloss.Width("q quit · Tab focus · / find · v view") {
		right = ""
		leftW = m.width
	}
	keys := "q quit · Tab focus · / find · v view"
	hints := []string{"F hosts", "↑↓ move", "⏎ zoom", "s sort", "X clear", "f stream", "h/c/p filter", "x/m/b/t filters", "T theme"}
	if m.view == 1 {
		hints = []string{"↑↓ move", "g/G ends", "⏎ zoom", "1 pools", "2 sources", "3/4 stream", "f stream"}
	}
	for _, hint := range hints {
		if lipgloss.Width(keys)+lipgloss.Width(hint)+3 > leftW {
			break
		}
		keys += " · " + hint
	}
	return styLabel.Render(keys) + strings.Repeat(" ", max(0, m.width-lipgloss.Width(keys)-lipgloss.Width(right))) + right
}

func (m Model) searchLine() string {
	return styLabel.Render("find: ") + m.search + styDim.Render("▏") +
		styFaint.Render("  (fuzzy · enter apply · esc cancel)")
}

func (m Model) body() string {
	if m.view == 1 {
		return m.servicesBody()
	}
	if m.zoom {
		return m.panel(m.focus, m.width, m.height-2)
	}
	streamH := clamp((m.height-2)*30/100, 4, 14)
	tablesH := (m.height - 2) - streamH
	clientsH := clamp(tablesH*38/100, 3, 24)
	topH := tablesH - clientsH

	gap := " "
	leftW := (m.width - 1) / 2
	rightW := m.width - 1 - leftW

	top := lipgloss.JoinHorizontal(lipgloss.Top,
		m.panel(0, leftW, topH),
		gap,
		m.panel(1, rightW, topH),
	)
	mid := m.panel(2, m.width, clientsH)
	bottom := m.panel(3, m.width, streamH)
	return strings.Join([]string{top, mid, bottom}, "\n")
}

func (m Model) panelTitle(panel int) string {
	var title, extra string
	switch panel {
	case 0:
		title = " HOSTS "
		if m.hostsFrozen {
			title += "[frozen] "
		}
		if len(m.filters.Hosts) > 0 {
			extra = " ●" + strconvLen(m.filters.Hosts)
		}
	case 1:
		title = " TOP URLS "
		if len(m.filters.Paths) > 0 {
			extra = " ●" + strconvLen(m.filters.Paths)
		}
	case 2:
		title = " CLIENTS "
		if len(m.filters.Clients) > 0 {
			extra = " ●" + strconvLen(m.filters.Clients)
		}
	case 3:
		title = " LIVE REQUESTS "
	}
	if panel < 3 && m.sorts[panel] != store.SortRate {
		extra += " ·" + m.sorts[panel].String()
	}
	return title + extra + " "
}

func (m Model) panel(panel int, w, h int) string {
	border := styBorderDim
	if m.view == 0 && panel == m.focus || panel == 3 && m.streamFocused() {
		border = styBorderFocus
	}
	contentW := w - 2
	if contentW < 8 {
		contentW = 8
	}
	viewH := max(0, h-3) // border 2 + title line 1

	var lines []string
	switch panel {
	case 0:
		lines = m.hostLines(contentW, viewH)
	case 1:
		lines = m.urlLines(contentW, viewH)
	case 2:
		lines = m.clientLines(contentW, viewH)
	case 3:
		lines = m.streamLines(contentW, viewH)
	}
	if viewH == 0 {
		lines = nil
	} else if len(lines) == 0 && panel != 3 {
		message := "waiting for requests…"
		if m.searchQuery(panel, false) != "" {
			message = "no matches"
		}
		lines = []string{styFaint.Render(message)}
	}

	var b strings.Builder
	sty := styTitleDim
	if panel == m.focus {
		sty = styTitle
	}
	title := m.panelTitle(panel)
	mw := lipgloss.NewStyle().MaxWidth(contentW)
	b.WriteString(mw.Render(sty.Render(title) + styFaint.Render(strings.Repeat("─", max(0, contentW-lipgloss.Width(sty.Render(title)))))))
	for _, l := range lines {
		b.WriteByte('\n')
		b.WriteString(mw.Render(l))
	}
	return border.Width(w).Height(h).MaxHeight(h).MaxWidth(w).Render(b.String())
}

func (m *Model) hostLines(w, viewH int) []string {
	rows := m.filteredRows(0)
	if len(rows) == 0 {
		return nil
	}
	filtered := len(m.filters.Hosts) > 0
	start := windowStart(m.sel[0], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		mark := " "
		vs := vhostStyle(r.Key)
		name := r.Key
		if filtered {
			if m.filters.Hosts[r.Key] {
				mark = "●"
				name = stySel.Render(" " + r.Key + " ")
			} else {
				name = styDim.Render(r.Key)
			}
			mark = styAccent.Render(mark)
		}
		nameW := w - 26
		if nameW < 6 {
			nameW = 6
		}
		row := mark + " " + padStyled(vs, name, nameW) +
			rightAligned(styLabel.Render(humanRate(r.Rate)), 8) +
			rightAligned(styLabel.Render(humanInt(r.Hits)), 8) +
			rightAligned(errStyle(r.Errs, r.Hits), 6)
		if i == m.sel[0] && m.focus == 0 {
			row = stySel.Render(row)
		}
		out = append(out, row)
	}
	return out
}

func (m *Model) anyLatency() bool {
	for _, r := range m.urls {
		if r.LatencyUs > 0 {
			return true
		}
	}
	return false
}

func (m *Model) urlLines(w, viewH int) []string {
	rows := m.filteredRows(1)
	if len(rows) == 0 {
		return nil
	}
	latCol := 0
	if m.anyLatency() {
		latCol = 7
	}
	pathFiltered := len(m.filters.Paths) > 0
	start := windowStart(m.sel[1], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		vhostW := min((w-23-latCol)/3, 14)
		pathW := w - 23 - vhostW - latCol
		if pathW < 8 {
			pathW = 8
		}
		mark := " "
		if pathFiltered {
			if m.filters.Paths[r.Path] {
				mark = "●"
			}
			mark = styAccent.Render(mark)
		}
		row := mark + styDim.Render(trunc(r.Method, 5)) + " " +
			padStyled(vhostStyle(r.Vhost), trunc(r.Vhost+":", vhostW), vhostW) +
			pad(styFaint, trunc(r.Path, pathW), pathW) +
			rightAligned(styLabel.Render(humanInt(r.Hits)), 8) +
			rightAligned(styLabel.Render(humanRate(r.Rate)), 7)
		if latCol > 0 {
			row += rightAligned(styLabel.Render(humanLatency(r.LatencyUs)), latCol)
		}
		if i == m.sel[1] && m.focus == 1 {
			row = stySel.Render(row)
		}
		out = append(out, row)
	}
	return out
}

func (m *Model) clientLines(w, viewH int) []string {
	rows := m.filteredRows(2)
	if len(rows) == 0 {
		return nil
	}
	ipFiltered := len(m.filters.Clients) > 0
	start := windowStart(m.sel[2], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		bot := " "
		if r.Bot {
			bot = styStatus4xx.Render("b")
		}
		uaW := w - 44
		if uaW < 4 {
			uaW = 4
		}
		mark := " "
		if ipFiltered {
			if m.filters.Clients[r.Key] {
				mark = "●"
			}
			mark = styAccent.Render(mark)
		}
		row := bot + " " + mark + padStyled(styLabel, r.Key, 16) +
			rightAligned(styLabel.Render(humanInt(r.Hits)), 8) +
			rightAligned(styLabel.Render(humanRate(r.Rate)), 7) +
			rightAligned(errStyle(r.Errs, r.Hits), 6) + " " +
			styFaint.Render(trunc(uaShort(r.UA), uaW))
		if i == m.sel[2] && m.focus == 2 {
			row = stySel.Render(row)
		}
		out = append(out, row)
	}
	return out
}

func (m *Model) streamLines(w, viewH int) []string {
	rows := m.streamRows()
	if len(rows) == 0 {
		if m.searchQuery(3, false) != "" {
			return []string{styFaint.Render("no matches")}
		}
		return nil
	}
	start := windowStart(m.sel[3], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		pathW := w - 62
		if pathW < 6 {
			pathW = 6
		}
		ss := statusStyle(r.Status)
		row := styStreamTime.Render(r.Time.Format("15:04:05")) + " " +
			padStyled(vhostStyle(r.Vhost), trunc(r.Vhost, 16), 16) +
			padStyled(styLabel, r.IP, 16) + " " +
			styDim.Render(trunc(r.Method, 4)) + " " +
			pad(styFaint, trunc(r.Path, pathW), pathW) +
			rightAligned(ss.Render(fmt.Sprintf("%d", r.Status)), 4) +
			rightAligned(styDim.Render(humanBytes(r.Bytes)), 7)
		if i == m.sel[3] && m.streamFocused() {
			row = stySel.Render(row)
		}
		out = append(out, row)
	}
	return out
}

// servicesBody renders the Services view: php-fpm pools + source health.
func (m Model) servicesBody() string {
	if m.serviceZoom {
		switch m.serviceFocus {
		case 0:
			return m.fpmPanel(m.width, m.height-2)
		case 1:
			return m.sourcesPanel(m.width, m.height-2)
		default:
			return m.panel(3, m.width, m.height-2)
		}
	}
	streamH := clamp((m.height-2)*30/100, 4, 14)
	tablesH := (m.height - 2) - streamH
	gap := " "
	leftW := (m.width - 1) / 2
	rightW := m.width - 1 - leftW
	fpmBox := m.fpmPanel(leftW, tablesH)
	srcBox := m.sourcesPanel(rightW, tablesH)
	top := lipgloss.JoinHorizontal(lipgloss.Top, fpmBox, gap, srcBox)
	bottom := m.panel(3, m.width, streamH)
	return strings.Join([]string{top, bottom}, "\n")
}

func (m Model) fpmPanel(w, h int) string {
	contentW := w - 2
	if contentW < 8 {
		contentW = 8
	}
	viewH := max(0, h-3)
	lines := m.fpmLines(contentW, viewH)
	var title string
	views := m.fpmSnapshot()
	if len(views) == 0 {
		title = " PHP-FPM "
		if len(lines) == 0 && viewH > 0 {
			lines = []string{styFaint.Render("no pools found")}
		}
	} else {
		title = fmt.Sprintf(" PHP-FPM (%d pools) ", len(views))
	}
	return m.boxed(0, title, lines, contentW, h)
}

func (m Model) sourcesPanel(w, h int) string {
	contentW := w - 2
	if contentW < 8 {
		contentW = 8
	}
	viewH := max(0, h-3)
	lines := m.sourceLines(contentW, viewH)
	title := " SOURCE HEALTH "
	return m.boxed(1, title, lines, contentW, h)
}

// boxed renders a bordered panel with title (unfocused dim styling).
func (m Model) boxed(panel int, title string, lines []string, contentW, h int) string {
	sty := styTitleDim
	border := styBorderDim
	if m.serviceFocus == panel {
		sty, border = styTitle, styBorderFocus
	}
	t := sty.Render(title)
	var b strings.Builder
	mw := lipgloss.NewStyle().MaxWidth(contentW)
	b.WriteString(mw.Render(t + styFaint.Render(strings.Repeat("─", max(0, contentW-lipgloss.Width(t))))))
	for _, l := range lines {
		b.WriteByte('\n')
		b.WriteString(mw.Render(l))
	}
	return border.Width(contentW + 2).Height(h).MaxWidth(contentW + 2).MaxHeight(h).Render(b.String())
}

func (m Model) fpmSnapshot() []fpm.PoolView {
	if m.fpmViews == nil {
		return nil
	}
	return m.fpmViews()
}

func (m Model) fpmLines(w, viewH int) []string {
	return m.serviceWindow(0, m.serviceEntries(0, w), viewH)
}

type serviceEntry struct {
	key, line string
}

func (m Model) poolEntries(w int) []serviceEntry {
	views := m.fpmSnapshot()
	var out []serviceEntry
	for _, v := range views {
		key := v.ConfFile + ":" + v.Name
		if w <= 0 { // Navigation needs identities/counts, not formatted metrics.
			out = append(out, serviceEntry{key: key})
			continue
		}
		state := styStatusOK.Render("●")
		switch {
		case v.Status != nil:
		case v.Disabled:
			state = styDim.Render("○")
		case v.Dead:
			state = styStatus5xx.Render("✗")
		}
		var queue string
		if v.Status != nil {
			if v.Status.ListenQueue > 0 {
				queue = styStatus5xx.Render(fmt.Sprintf("q:%d", v.Status.ListenQueue))
			} else {
				queue = styLabel.Render("q:0")
			}
		}
		var row string
		if v.Status != nil {
			row = fmt.Sprintf("%s %-10s %-9s a:%d/%d %s slow:%d %s",
				state, trunc(v.Name, 10), v.PMMode,
				v.Status.ActiveProcesses, v.Status.TotalProcesses,
				queue, v.Status.SlowRequests,
				styDim.Render("est-mem:"+humanBytes(v.RSSKB*1024)))
		} else {
			detail := v.Err
			if detail == "" {
				detail = "status unavailable"
			}
			wrk := ""
			if v.Workers > 0 {
				wrk = styLabel.Render(fmt.Sprintf(" %dw", v.Workers))
			}
			row = fmt.Sprintf("%s %-10s%s %s", state, trunc(v.Name, 10), wrk, styFaint.Render(trunc(detail, w-24)))
		}
		row += fmt.Sprintf(" log:%d slowlog:%d", v.Access.Requests, v.SlowSeen)
		if v.Access.DurationCount > 0 {
			row += " avg:" + humanLatency(v.Access.DurationUs/v.Access.DurationCount)
		}
		if v.Access.MemoryCount > 0 {
			row += " req-mem:" + humanBytes(v.Access.MemoryBytes/v.Access.MemoryCount)
		}
		if v.Access.Bad > 0 {
			row += fmt.Sprintf(" bad:%d", v.Access.Bad)
		}
		if v.AccessErr != "" {
			row += " access:" + v.AccessErr
		}
		if v.SlowErr != "" {
			row += " slowlog:" + v.SlowErr
		}
		if v.SlowLag {
			row += " slowlog:catching-up"
		}
		out = append(out, serviceEntry{key: key, line: row})
	}
	return out
}

func (m Model) sourceLines(w, viewH int) []string {
	return m.serviceWindow(1, m.serviceEntries(1, w), viewH)
}

func (m Model) sourceEntries(w int) []serviceEntry {
	if m.tailer == nil {
		return nil
	}
	diagnostics := m.tailer.Errors()
	srcs := m.tailer.Sources()
	var out []serviceEntry
	live, replay := 0, 0
	for _, s := range srcs {
		if s.Replay {
			replay++
		} else {
			live++
		}
	}
	out = append(out, serviceEntry{key: "summary", line: styLabel.Render(fmt.Sprintf("live:%d replay:%d", live, replay))})
	for _, path := range sortedStringKeys(diagnostics) {
		out = append(out, serviceEntry{key: "error:" + path, line: styStatus4xx.Render(trunc(diagnostics[path], w))})
	}
	for _, s := range srcs {
		kind := "R"
		if !s.Replay {
			kind = styStatusOK.Render("L")
		}
		out = append(out, serviceEntry{key: "source:" + s.Path, line: fmt.Sprintf(" %s %s %s",
			kind,
			padStyled(vhostStyle(s.Vhost), trunc(s.Vhost, 14), 14),
			styFaint.Render(trunc(s.Path, w-22)))})
	}
	return out
}

func (m Model) serviceEntries(panel, w int) []serviceEntry {
	var entries []serviceEntry
	if panel == 0 {
		entries = m.poolEntries(w)
	} else {
		entries = m.sourceEntries(w)
	}
	if query := m.searchQuery(panel, true); query != "" {
		out := entries[:0]
		for _, entry := range entries {
			if fuzzy(query, entry.key) {
				out = append(out, entry)
			}
		}
		return out
	}
	return entries
}

func (m Model) serviceWindow(panel int, entries []serviceEntry, viewH int) []string {
	if viewH <= 0 {
		return nil
	}
	if len(entries) == 0 && m.searchQuery(panel, true) != "" {
		return []string{styFaint.Render("no matches")}
	}
	start := windowStart(m.serviceSel[panel], len(entries), viewH)
	var out []string
	for i := start; i < len(entries) && len(out) < viewH; i++ {
		line := entries[i].line
		if m.view == 1 && m.serviceFocus == panel && i == m.serviceSel[panel] {
			line = stySel.Render(line)
		}
		out = append(out, line)
	}
	return out
}

func (m *Model) refreshServices() {
	for panel := 0; panel < 2; panel++ {
		var selected string
		if i := m.serviceSel[panel]; i > 0 && i < len(m.serviceKeys[panel]) {
			selected = m.serviceKeys[panel][i]
		}
		entries := m.serviceEntries(panel, 0)
		keys := make([]string, len(entries))
		m.serviceSel[panel] = 0
		for i, entry := range entries {
			keys[i] = entry.key
			if selected != "" && entry.key == selected {
				m.serviceSel[panel] = i
			}
		}
		m.serviceKeys[panel] = keys
	}
}

func windowStart(sel, total, viewH int) int {
	if total <= viewH {
		return 0
	}
	start := sel - viewH/2
	if start < 0 {
		start = 0
	}
	if start > total-viewH {
		start = total - viewH
	}
	return start
}

func errStyle(errs, hits int64) string {
	if hits == 0 || errs == 0 {
		return "0%"
	}
	pct := errs * 100 / hits
	switch {
	case pct >= 20:
		return styStatus5xx.Render(fmt.Sprintf("%d%%", pct))
	case pct >= 5:
		return styStatus4xx.Render(fmt.Sprintf("%d%%", pct))
	default:
		return styLabel.Render(fmt.Sprintf("%d%%", pct))
	}
}

func uaShort(ua string) string {
	ua = strings.TrimPrefix(ua, "Mozilla/5.0 ")
	if i := strings.IndexByte(ua, ' '); i > 0 && strings.HasSuffix(ua[:i], ")") {
		rest := ua[i+1:]
		if j := strings.IndexByte(rest, ' '); j > 0 {
			return rest[:j]
		}
		return rest
	}
	return ua
}

// padStyled renders s with style, padded with plain spaces to width w.
func padStyled(style lipgloss.Style, s string, w int) string {
	return style.Render(trunc(s, w)) + strings.Repeat(" ", max(0, w-utf8.RuneCountInString(s)))
}

func pad(style lipgloss.Style, s string, w int) string {
	n := max(0, w-utf8.RuneCountInString(s))
	return style.Render(trunc(s, w)) + strings.Repeat(" ", n)
}

func rightAligned(s string, w int) string {
	n := max(0, w-lipgloss.Width(s))
	return strings.Repeat(" ", n) + s
}

func trunc(s string, w int) string {
	if w < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	r := []rune(s)
	return string(r[:w-1]) + "…"
}

func humanInt(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func humanRate(r float64) string {
	if r < 10 {
		return fmt.Sprintf("%.1f/s", r)
	}
	return fmt.Sprintf("%d/s", int(r))
}

func humanLatency(us int64) string {
	switch {
	case us <= 0:
		return "-"
	case us < 1000:
		return fmt.Sprintf("%dµs", us)
	case us < 1000000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	default:
		return fmt.Sprintf("%.1fs", float64(us)/1e6)
	}
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
