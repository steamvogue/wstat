// wstat: realtime per-vhost access-log monitor for Apache/nginx hosts.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	tea "charm.land/bubbletea/v2"
	"flag"

	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/parser"
	"github.com/steamvogue/wstat/internal/store"
	"github.com/steamvogue/wstat/internal/ui"
)

// version is set at build time via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	// Keep the dashboard's memory footprint bounded even under big seed
	// replays and bot-heavy unique-path storms.
	debug.SetMemoryLimit(48 << 20)

	seedN := flag.Int("n", 1000, "lines to seed per file on startup")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("wstat %s\n", version)
		return
	}

	globs := flag.Args()
	searched := logsrc.DefaultGlobs
	if len(globs) > 0 {
		searched = globs
	}
	sources := logsrc.Discover(globs)
	if len(sources) == 0 {
		fmt.Fprintln(os.Stderr, "wstat: no access logs found")
		fmt.Fprintf(os.Stderr, "  searched: %s\n", strings.Join(searched, " "))
		fmt.Fprintln(os.Stderr, "usage: wstat [glob ...]   e.g. wstat '/var/log/apache2/*-access.log'")
		os.Exit(1)
	}

	tailer := logsrc.Start(globs, *seedN)
	st := store.New()
	go func() {
		for line := range tailer.Ch {
			r, ok := parser.Parse(line.Text, line.Source.Vhost)
			if !ok {
				st.AddBad()
				continue
			}
			if line.Seeded {
				st.AddSeed(r)
			} else {
				st.Add(r)
			}
		}
	}()

	if _, err := tea.NewProgram(ui.New(st, tailer)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wstat:", err)
		os.Exit(1)
	}
	tailer.Stop()
}
