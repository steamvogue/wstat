package wizard

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func items(n int) []Item {
	var out []Item
	for i := 0; i < n; i++ {
		out = append(out, Item{Path: "/p", Vhost: "v", Live: true})
	}
	return out
}

func drive(m Model, msgs ...tea.KeyPressMsg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s} }

func TestToggleAndSave(t *testing.T) {
	m := New(items(3))
	if len(m.Selected()) != 3 {
		t.Fatal("all items must start checked")
	}
	// Uncheck the first item, save.
	m = drive(m, key(" "))
	if len(m.Selected()) != 2 {
		t.Fatalf("selected = %d, want 2", len(m.Selected()))
	}
	m = drive(m, key("enter"))
	if !m.Saved || m.Aborted {
		t.Fatalf("saved=%v aborted=%v", m.Saved, m.Aborted)
	}
}

func TestEnterWithNothingSelectedRefuses(t *testing.T) {
	m := New(items(2))
	// 'a' toggles all off (all were on).
	m = drive(m, key("a"))
	if len(m.Selected()) != 0 {
		t.Fatalf("selected = %d, want 0", len(m.Selected()))
	}
	m = drive(m, key("enter"))
	if m.Saved {
		t.Fatal("must refuse to save an empty selection")
	}
}

func TestToggleAllBack(t *testing.T) {
	m := New(items(2))
	m = drive(m, key("a")) // all off
	m = drive(m, key("a")) // all on
	if len(m.Selected()) != 2 {
		t.Fatalf("selected = %d, want 2", len(m.Selected()))
	}
}

func TestAbort(t *testing.T) {
	m := New(items(2))
	m = drive(m, key("q"))
	if !m.Aborted || m.Saved {
		t.Fatalf("aborted=%v saved=%v", m.Aborted, m.Saved)
	}
}

func TestNavigation(t *testing.T) {
	m := New(items(3))
	m = drive(m, key("j"), key("j"), key("k"))
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	m = drive(m, key("G"))
	if m.cursor != 2 {
		t.Fatalf("G cursor = %d, want 2", m.cursor)
	}
	m = drive(m, key("g"))
	if m.cursor != 0 {
		t.Fatalf("g cursor = %d, want 0", m.cursor)
	}
	// Toggle on the moved cursor affects that item.
	m = drive(m, key("j"), key(" "))
	if m.checked[1] {
		t.Fatal("second item should be unchecked")
	}
}

func TestViewRenders(t *testing.T) {
	m := New(items(2))
	out := m.View()
	s := out.Content
	for _, want := range []string{"[x]", "/p", "space toggle", "wstat init"} {
		if !contains(s, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
