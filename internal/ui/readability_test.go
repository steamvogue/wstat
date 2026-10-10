package ui

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steamvogue/wstat/internal/parser"
	"github.com/steamvogue/wstat/internal/store"
)

func readabilityModel() Model {
	s := store.New()
	for i, r := range []parser.Record{
		{Vhost: "shop.example", IP: "192.0.2.10", Path: "/products/winter-jacket", UA: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"},
		{Vhost: "docs.example", IP: "192.0.2.20", Path: "/guides/getting-started", Bot: true, UA: "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"},
		{Vhost: "api.example", IP: "2001:db8::25", Path: "/api/orders/12345", UA: "curl/8.5.0", Bot: true},
		{Vhost: "shop.example", IP: "192.0.2.30", Path: "/assets/images/catalogue/autumn-collection/large/winter-jacket-blue-front.webp", UA: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1"},
		{Vhost: "docs.example", IP: "192.0.2.40", Path: "/robots.txt", Bot: true, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Amazonbot/0.1; +https://developer.amazon.com/support/amazonbot)"},
	} {
		r.Method, r.Status, r.Bytes = "GET", 200, 1024
		r.Time = time.Date(2026, 10, 10, 12, 0, i, 0, time.UTC)
		s.Add(r)
	}
	m := New(s, nil, nil)
	m.width, m.height = 180, 36
	m.refresh()
	return m
}

func TestStreamURLAndAgentLayout(t *testing.T) {
	for _, width := range []int{38, 58, 78, 98, 118, 158, 198, 248} {
		for _, path := range []string{"/orders/12345", "/orders/" + strings.Repeat("very-long-section/", 20), "/orders/" + strings.Repeat("注文", 30)} {
			t.Run(fmt.Sprintf("width%d/%s", width, path[:8]), func(t *testing.T) {
				m := readabilityModel()
				m.stream = []parser.Record{{Vhost: "shop.example", IP: "2001:db8:1234:5678::1", Method: "POST", Path: path, Status: 200, Bytes: 1024, Bot: true, UA: "Mozilla/5.0 (compatible; Googlebot/2.1; +http://google.com/bot.html)"}}
				row := m.streamLines(width, 1)[0]
				plain := ansi.Strip(row)
				if lipgloss.Width(row) != width || !strings.Contains(plain, "/orders/") || !strings.HasSuffix(plain, " 200   1.0K") {
					t.Fatalf("URL or status/bytes lost at width %d (%d cells): %q", width, lipgloss.Width(row), plain)
				}
				if !strings.HasPrefix(plain, "b ") {
					t.Fatal("bot marker missing")
				}
				if width >= 158 && path == "/orders/12345" && !strings.Contains(plain, "Googlebot/2.1") {
					t.Fatal("agent missing despite enough room beside the URL")
				}
				if path == "/orders/12345" && !strings.Contains(plain, path) {
					t.Fatal("agent took space from a short URL")
				}
				m.stream[0].Bot, m.stream[0].UA = false, ""
				plain = ansi.Strip(m.streamLines(width, 1)[0])
				if !strings.HasPrefix(plain, "  ") || strings.Contains(plain, " · ") {
					t.Fatal("human request or empty agent got a misleading marker")
				}
			})
		}
	}
}

func TestStreamPreservesCompleteURLBeforeAgent(t *testing.T) {
	m := readabilityModel()
	m.stream = m.stream[:1]
	m.stream[0].Path = "/orders/" + strings.Repeat("x", 84)
	plain := ansi.Strip(m.streamLines(158, 1)[0])
	if !strings.Contains(plain, m.stream[0].Path) || strings.Contains(plain, " · ") {
		t.Fatal("agent shortened a URL that fits when the agent is omitted")
	}
}

func TestStreamAgentIdentity(t *testing.T) {
	for _, tc := range []struct{ ua, want string }{
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://google.com/bot.html)", "Googlebot/2.1"},
		{"Mozilla/5.0 Chrome/123 Safari/537.36 Edg/123", "Edg/123"},
		{"Mozilla/5.0 Gecko/20100101 Firefox/124.0", "Firefox/124.0"},
		{"Mozilla/5.0 Version/18.0 Mobile/15E148 Safari/604.1", "Safari/604.1"},
		{"curl/8.5.0", "curl/8.5.0"},
		{"python-requests/2.32", "python-requests/2.32"},
		{"", ""},
		{"-", ""},
		{"  ", ""},
	} {
		if got := streamAgent(tc.ua); got != tc.want {
			t.Errorf("agent for %q = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	linear := func(v uint32) float64 {
		x := float64(v) / 65535
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return .2126*linear(r) + .7152*linear(g) + .0722*linear(b)
}

func TestReadabilityThemes(t *testing.T) {
	t.Cleanup(func() { applyTheme(0) })
	for i := range themes {
		applyTheme(i)
		text := luminance(styText.GetForeground())
		if (text+.05)/.05 < 7 || (text+.05)/(luminance(stySel.GetBackground())+.05) < 4.5 {
			t.Errorf("theme %d: request text has insufficient contrast on black or selected rows", i)
		}
		m := readabilityModel()
		out := m.render()
		if lipgloss.Width(out) > m.width || lipgloss.Height(out) != m.height {
			t.Errorf("theme %d: frame exceeds terminal dimensions", i)
		}
		// Optional faithful render capture for local visual review.
		if folder := os.Getenv("WSTAT_UI_PREVIEW_DIR"); folder != "" {
			if err := os.MkdirAll(folder, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("theme-%d.ansi", i)), []byte(out), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
