// wstat: realtime per-vhost access-log monitor for Apache/nginx hosts.
package main

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"flag"

	"wstat/internal/logsrc"
	"wstat/internal/parser"
	"wstat/internal/store"
	"wstat/internal/ui"
)

func main() {
	seedN := flag.Int("n", 1000, "lines to seed per file on startup")
	flag.Parse()

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
			if r, ok := parser.Parse(line.Text, line.Source.Vhost); ok {
				st.Add(r)
			} else {
				st.AddBad()
			}
		}
	}()

	if _, err := tea.NewProgram(ui.New(st, tailer)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wstat:", err)
		os.Exit(1)
	}
	tailer.Stop()
}
