// Package ui renders the wstat dashboard: header, HOSTS/URLS/CLIENTS
// panels, live request stream, filters and colors.
package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wstat/internal/logsrc"
	"wstat/internal/parser"
	"wstat/internal/store"
)

const refreshInterval = 500 * time.Millisecond

type tickMsg time.Time

var (
	styBorderFocus = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#e8a33d"))
	styBorderDim   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#4a4a5a"))

	styTitle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8a33d"))
	styTitleDim   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#6a6a7a"))
	styLogo       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8a33d"))
	styLabel      = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a9a"))
	styDim        = lipgloss.NewStyle().Foreground(lipgloss.Color("#6a6a7a"))
	styFaint      = lipgloss.NewStyle().Foreground(lipgloss.Color("#454555"))
	styAccent     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8a33d"))
	styChip       = lipgloss.NewStyle().Foreground(lipgloss.Color("#0d0d12")).Background(lipgloss.Color("#e8a33d"))
	styStatusOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("#41c98e"))
	styStatus3xx  = lipgloss.NewStyle().Foreground(lipgloss.Color("#40c4ff"))
	styStatus4xx  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f2c14e"))
	styStatus5xx  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f05a5a"))
	stySel        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#3a3a4a"))
	styStreamTime = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a9a"))

	hostPalette = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("#e8a33d")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#41c98e")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#40c4ff")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#c792ea")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#f78c6c")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#89ddff")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5370")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#a9dc76")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#bb80b3")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#aed581")),
	}
)

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
	st         *store.Store
	tailer     *logsrc.Tailer
	width      int
	height     int
	focus      int // 0 hosts, 1 urls, 2 clients, 3 stream
	zoom       bool
	sel        [4]int
	hostSel    map[string]bool
	status     store.StatusMask
	search     string
	searchMode bool

	hosts   []store.Row
	urls    []store.Row
	clients []store.Row
	stream  []parser.Record
	tot     store.Totals
	bad     int64
	started time.Time
}

func New(st *store.Store, tailer *logsrc.Tailer) Model {
	return Model{st: st, tailer: tailer, hostSel: map[string]bool{}, started: time.Now()}
}

func (m Model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) refresh() {
	// Keep the stream pinned to the bottom unless the user scrolled up.
	atBottom := len(m.stream) == 0 || m.sel[3] >= len(m.stream)-1

	m.hosts, m.urls, m.clients, m.stream, m.tot, m.bad =
		m.st.Snapshot(m.hostSel, m.status, 200)

	if atBottom && len(m.stream) > 0 {
		m.sel[3] = len(m.stream) - 1
	}
	m.clampSel()
}

func (m *Model) clampSel() {
	m.sel[0] = clamp(m.sel[0], 0, len(m.hosts)-1)
	m.sel[1] = clamp(m.sel[1], 0, len(m.urls)-1)
	m.sel[2] = clamp(m.sel[2], 0, len(m.clients)-1)
	m.sel[3] = clamp(m.sel[3], 0, len(m.stream)-1)
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
			m.search = m.search[:n-1]
		}
	default:
		if msg.Text != "" {
			m.search += msg.Text
		}
	}
	m.sel = [4]int{}
}

func (m *Model) navKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c", "q":
		return tea.Quit, true
	case "tab":
		m.focus = (m.focus + 1) % 4
		return nil, true
	case "shift+tab":
		m.focus = (m.focus + 3) % 4
		return nil, true
	case "enter":
		m.zoom = !m.zoom
		return nil, true
	case "esc":
		m.zoom = false
		return nil, true
	case "1", "2", "3", "4":
		m.focus = int(msg.String()[0] - '1')
		return nil, true
	case "j", "down":
		m.sel[m.focus]++
		m.clampSel()
	case "k", "up":
		m.sel[m.focus]--
		m.clampSel()
	case "g":
		m.sel[m.focus] = 0
	case "G":
		if m.focus == 3 {
			m.sel[3] = len(m.stream) - 1
		} else {
			rows := m.focusRows()
			m.sel[m.focus] = len(rows) - 1
		}
	case "/":
		m.searchMode = true
		return nil, true
	case "h":
		if m.focus == 0 {
			rows := m.filteredRows(0)
			if i := clamp(m.sel[0], 0, len(rows)-1); len(rows) > 0 {
				v := rows[i].Key
				if m.hostSel[v] {
					delete(m.hostSel, v)
				} else {
					m.hostSel[v] = true
				}
			}
		}
	case "x":
		switch m.status {
		case 0:
			m.status = store.MaskErr
		case store.MaskErr:
			m.status = store.MaskOK
		default:
			m.status = 0
		}
	case "X":
		m.hostSel = map[string]bool{}
		m.status = 0
		m.search = ""
		m.sel = [4]int{}
	default:
		return nil, false
	}
	return nil, true
}

// focusRows returns the rows of the focused panel with the panel-local
// search filter applied.
func (m *Model) focusRows() []store.Row {
	return m.filteredRows(m.focus)
}

func (m *Model) filteredRows(panel int) []store.Row {
	q := strings.ToLower(m.search)
	if q == "" {
		return m.rawRows(panel)
	}
	var out []store.Row
	for _, r := range m.rawRows(panel) {
		if strings.Contains(strings.ToLower(r.Key+" "+r.Path+" "+r.UA), q) {
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
	b.WriteString(m.body())
	b.WriteByte('\n')
	if m.searchMode {
		b.WriteString(m.searchLine())
	} else {
		b.WriteString(m.footer())
	}
	return b.String()
}

func (m Model) header() string {
	var b strings.Builder
	b.WriteString(styLogo.Render("wstat"))
	b.WriteByte(' ')
	if len(m.hosts) > 0 {
		b.WriteString(styLabel.Render(fmt.Sprintf("%d hosts", len(m.hosts))))
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
	left := lipgloss.NewStyle().Width(m.width - lipgloss.Width(clock)).Render(b.String())
	return left + clock
}

func (m Model) chips() []string {
	var chips []string
	if len(m.hostSel) > 0 {
		var vs []string
		for v := range m.hostSel {
			vs = append(vs, v)
		}
		chips = append(chips, "host:"+strings.Join(vs, ","))
	}
	switch m.status {
	case store.MaskErr:
		chips = append(chips, "4xx-5xx")
	case store.MaskOK:
		chips = append(chips, "2xx-3xx")
	}
	if m.search != "" {
		chips = append(chips, "/"+m.search)
	}
	return chips
}

func (m Model) footer() string {
	keys := styLabel.Render("q quit · tab 1-4 focus · ⏎ zoom · j/k g/G move · / search · h host · x status · X clear")
	right := styLabel.Render(fmt.Sprintf("src:%d", len(m.tailer.Sources())))
	if m.bad > 0 {
		right += styDim.Render(" · ") + styStatus4xx.Render(fmt.Sprintf("bad:%d", m.bad))
	}
	elapsed := time.Since(m.started).Round(time.Second)
	right += styDim.Render(fmt.Sprintf(" · %s", elapsed))
	leftW := m.width - lipgloss.Width(right)
	if leftW < 1 {
		leftW = 1
	}
	return lipgloss.NewStyle().MaxWidth(leftW).Render(keys) + right
}

func (m Model) searchLine() string {
	return styLabel.Render("search: ") + m.search + styDim.Render("▏") +
		styFaint.Render("  (enter apply · esc cancel)")
}

func (m Model) body() string {
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

func (m Model) panel(panel int, w, h int) string {
	border := styBorderDim
	if panel == m.focus {
		border = styBorderFocus
	}
	contentW := w - 2
	if contentW < 8 {
		contentW = 8
	}
	viewH := h - 3 // border 2 + title line 1
	if viewH < 1 {
		viewH = 1
	}

	var title string
	var lines []string
	switch panel {
	case 0:
		title = " HOSTS "
		lines = m.hostLines(contentW, viewH)
	case 1:
		title = " TOP URLS "
		lines = m.urlLines(contentW, viewH)
	case 2:
		title = " CLIENTS "
		lines = m.clientLines(contentW, viewH)
	case 3:
		title = " LIVE REQUESTS "
		lines = m.streamLines(contentW, viewH)
	}
	if len(lines) == 0 && panel != 3 {
		lines = []string{styFaint.Render("waiting for requests…")}
	}

	var b strings.Builder
	b.WriteString(m.titleLine(title, contentW, panel == m.focus))
	mw := lipgloss.NewStyle().MaxWidth(contentW)
	for _, l := range lines {
		b.WriteByte('\n')
		b.WriteString(mw.Render(l))
	}
	return border.Width(contentW).Height(h - 2).Render(b.String())
}

func (m Model) titleLine(title string, w int, focused bool) string {
	sty := styTitleDim
	if focused {
		sty = styTitle
	}
	return sty.Render(title) + styFaint.Render(strings.Repeat("─", max(0, w-lipgloss.Width(sty.Render(title)))))
}

func (m *Model) hostLines(w, viewH int) []string {
	rows := m.filteredRows(0)
	if len(rows) == 0 {
		return nil
	}
	filtered := len(m.hostSel) > 0
	start := windowStart(m.sel[0], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		mark := " "
		vs := vhostStyle(r.Key)
		name := r.Key
		if filtered {
			if m.hostSel[r.Key] {
				mark = "●"
				name = stySel.Render(" " + r.Key + " ") // selection marking
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

func (m *Model) urlLines(w, viewH int) []string {
	rows := m.filteredRows(1)
	if len(rows) == 0 {
		return nil
	}
	start := windowStart(m.sel[1], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		pathW := w - 23
		if pathW < 8 {
			pathW = 8
		}
		row := styDim.Render(trunc(r.Method, 5)) + " " +
			padStyled(vhostStyle(r.Vhost), r.Vhost+":", min(pathW/3, 14)) +
			pad(styFaint, trunc(r.Path, pathW-min(pathW/3, 14)), pathW-min(pathW/3, 14)) +
			rightAligned(styLabel.Render(humanInt(r.Hits)), 8) +
			rightAligned(styLabel.Render(humanRate(r.Rate)), 7)
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
	start := windowStart(m.sel[2], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		bot := " "
		if r.Bot {
			bot = styStatus4xx.Render("b")
		}
		uaW := w - 42
		if uaW < 4 {
			uaW = 4
		}
		row := bot + " " + padStyled(styLabel, r.Key, 16) +
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
	q := strings.ToLower(m.search)
	var rows []parser.Record
	for _, r := range m.stream {
		if q != "" && !strings.Contains(strings.ToLower(r.Vhost+" "+r.Path+" "+r.IP), q) {
			continue
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		if q != "" {
			return []string{styFaint.Render("no matches")}
		}
		return nil
	}
	start := windowStart(m.sel[3], len(rows), viewH)
	var out []string
	for i := start; i < len(rows) && len(out) < viewH; i++ {
		r := rows[i]
		pathW := w - 60
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
		if i == m.sel[3] && m.focus == 3 {
			row = stySel.Render(row)
		}
		out = append(out, row)
	}
	return out
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
