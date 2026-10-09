// wstat: realtime per-vhost access-log monitor for Apache/nginx hosts.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/steamvogue/wstat/internal/detect"
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

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "doctor":
			fmt.Print(detect.Run().Doctor())
			return
		case "detect":
			runDetect(os.Args[2:])
			return
		case "version":
			fmt.Printf("wstat %s\n", version)
			return
		}
	}

	seedN := flag.Int("n", 1000, "lines to seed per file on startup")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("wstat %s\n", version)
		return
	}

	globs := flag.Args()
	rescanGlobs := logsrc.DefaultGlobs
	var sources []logsrc.Source
	var vhostMap map[string]string

	if len(globs) > 0 {
		// Explicit globs: respect them exactly.
		sources = logsrc.Discover(globs, nil)
		rescanGlobs = globs
	} else {
		// Zero-config: detect the host layout (config scan gives exact
		// vhost attribution), fall back to the standard globs.
		rep := detect.Run()
		sources = rep.Sources()
		vhostMap = rep.VhostMap()
	}

	searched := rescanGlobs
	if len(globs) > 0 {
		searched = globs
	}
	if len(sources) == 0 {
		fmt.Fprintln(os.Stderr, "wstat: no access logs found")
		fmt.Fprintf(os.Stderr, "  searched: %s\n", strings.Join(searched, " "))
		fmt.Fprintln(os.Stderr, "run `wstat doctor` to see what was detected on this host")
		fmt.Fprintln(os.Stderr, "usage: wstat [glob ...]   e.g. wstat '/var/log/apache2/*-access.log'")
		os.Exit(1)
	}

	tailer := logsrc.Start(sources, rescanGlobs, *seedN, vhostMap)
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

func runDetect(args []string) {
	fs := flag.NewFlagSet("detect", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit the raw detection report as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "wstat detect:", err)
		os.Exit(2)
	}
	rep := detect.Run()
	if *asJSON {
		fmt.Println(rep.JSON())
		return
	}
	fmt.Print(rep.Doctor())
}
