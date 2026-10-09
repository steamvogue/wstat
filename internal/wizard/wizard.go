// Package wizard implements the `wstat init` source-selection UI: a
// checkbox list over detected log sources.
package wizard

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Item is one selectable log source.
type Item struct {
	Path   string
	Vhost  string
	Live   bool
	Format string
	Ratio  float64
}

// Model is the wizard bubbletea model.
type Model struct {
	Items   []Item
	checked []bool
	cursor  int
	Saved   bool
	Aborted bool
}

// New builds a wizard with all items checked by default.
func New(items []Item) Model {
	checked := make([]bool, len(items))
	for i := range checked {
		checked[i] = true
	}
	return Model{Items: items, checked: checked}
}

// Selected returns the checked items.
func (m Model) Selected() []Item {
	var out []Item
	for i, c := range m.checked {
		if c {
			out = append(out, m.Items[i])
		}
	}
	return out
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		_ = msg
	case tea.KeyPressMsg:
		s := msg.String()
		if msg.Text == " " {
			s = "space" // space arrives as Text or Code depending on input driver
		}
		switch s {
		case "ctrl+c", "q", "esc":
			m.Aborted = true
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.Items)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.Items) - 1
		case " ", "space":
			if len(m.checked) > 0 {
				m.checked[m.cursor] = !m.checked[m.cursor]
			}
		case "a":
			all := true
			for _, c := range m.checked {
				if !c {
					all = false
					break
				}
			}
			for i := range m.checked {
				m.checked[i] = !all
			}
		case "enter":
			if len(m.Selected()) == 0 {
				return m, nil // nothing selected: refuse to save
			}
			m.Saved = true
			return m, tea.Quit
		}
	}
	return m, nil
}

var (
	styTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8a33d"))
	styDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6a6a7a"))
	stySel   = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("#3a3a4a"))
	styFaint = lipgloss.NewStyle().Foreground(lipgloss.Color("#454555"))
)

func (m Model) View() tea.View {
	var b strings.Builder
	b.WriteString(styTitle.Render(" wstat init — select the log sources to monitor"))
	b.WriteString("\n\n")
	for i, it := range m.Items {
		mark := "[x]"
		if !m.checked[i] {
			mark = "[ ]"
		}
		line := fmt.Sprintf(" %s %s  %-16s %s", mark, it.Path, it.Vhost, styFaint.Render(it.Format))
		if i == m.cursor {
			line = stySel.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styDim.Render(" space toggle · a all/none · j/k move · enter save · q abort"))
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}
