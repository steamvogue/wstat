// wstat: realtime per-vhost access-log monitor for Apache/nginx hosts.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steamvogue/wstat/internal/config"
	"github.com/steamvogue/wstat/internal/detect"
	"github.com/steamvogue/wstat/internal/fpm"
	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/parser"
	"github.com/steamvogue/wstat/internal/store"
	"github.com/steamvogue/wstat/internal/ui"
	"github.com/steamvogue/wstat/internal/wizard"
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
			fmt.Print(runDoctor())
			return
		case "detect":
			runDetect(os.Args[2:])
			return
		case "config":
			runConfig(os.Args[2:])
			return
		case "init":
			runInit(os.Args[2:])
			return
		case "version":
			fmt.Printf("wstat %s\n", version)
			return
		}
	}

	seedN := flag.Int("n", 0, "lines to seed per file on startup (overrides config)")
	redetect := flag.Bool("redetect", false, "force a fresh host detection, ignoring the cache")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("wstat %s\n", version)
		return
	}

	loaded := config.Load()
	cfg := loaded.Config
	seed := cfg.Source.SeedLines
	if *seedN > 0 {
		seed = *seedN
	}

	globs := flag.Args()
	var sources []logsrc.Source
	var vhostMap map[string]string
	rescanGlobs := logsrc.DefaultGlobs

	switch {
	case len(globs) > 0:
		// Explicit CLI globs win over everything.
		sources = logsrc.Discover(globs, cfg.Source.Vhost)
		rescanGlobs = globs
	case len(cfg.Source.Paths) > 0:
		// Configured paths: detection skipped for source selection.
		sources = logsrc.Discover(cfg.Source.Paths, cfg.Source.Vhost)
		rescanGlobs = cfg.Source.Paths
		vhostMap = cfg.Source.Vhost
	case cfg.Detect.Enabled:
		rep, _ := detect.RunCached(*redetect || !cfg.Detect.Cache)
		sources = rep.Sources()
		vhostMap = rep.VhostMap()
		// Config vhost pins override detection attribution.
		for k, v := range cfg.Source.Vhost {
			vhostMap[k] = v
		}
	default:
		sources = logsrc.Discover(nil, nil)
	}

	if len(sources) == 0 {
		fmt.Fprintln(os.Stderr, "wstat: no access logs found")
		fmt.Fprintf(os.Stderr, "  searched: %s\n", strings.Join(rescanGlobs, " "))
		fmt.Fprintln(os.Stderr, "run `wstat doctor` to see what was detected on this host")
		fmt.Fprintln(os.Stderr, "usage: wstat [glob ...]   e.g. wstat '/var/log/apache2/*-access.log'")
		os.Exit(1)
	}

	// php-fpm: pools with access logs that record durations become extra
	// sources (vhost attribution = pool name); the poller feeds the
	// Services view.
	fpmPools := fpm.DiscoverPools()
	for _, pool := range fpmPools {
		if pool.HasLatency() && fileExists(pool.AccessLog) {
			sources = append(sources, logsrc.Source{Path: pool.AccessLog, Vhost: pool.Name})
		}
	}
	var fpmViews func() []fpm.PoolView
	var fpmPoller *fpm.Poller
	if len(fpmPools) > 0 {
		fpmPoller = fpm.NewPoller(fpmPools, 2*time.Second)
		defer fpmPoller.Stop()
		poller := fpmPoller
		fpmViews = poller.Views
	}

	tailer := logsrc.Start(sources, rescanGlobs, seed, vhostMap)
	st := store.New()
	go func() {
		for line := range tailer.Ch {
			r, ok := parser.Parse(line.Text, line.Source.Vhost)
			if !ok {
				// php-fpm access.log lines (no brackets around the time).
				r, ok = fpm.ParseAccess(line.Text, line.Source.Vhost)
			}
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

	if _, err := tea.NewProgram(ui.New(st, tailer, fpmViews)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wstat:", err)
		os.Exit(1)
	}
	tailer.Stop()
}

func runDetect(args []string) {
	fs := flag.NewFlagSet("detect", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit the raw detection report as JSON")
	force := fs.Bool("redetect", false, "ignore the detection cache")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "wstat detect:", err)
		os.Exit(2)
	}
	rep, _ := detect.RunCached(*force)
	if *asJSON {
		fmt.Println(rep.JSON())
		return
	}
	fmt.Print(rep.Doctor())
}

func runDoctor() string {
	text := detect.Run().Doctor()
	text += fpm.DoctorText(fpm.DiscoverPools())
	text += "detection:   " + detect.CacheStatus() + "\n"
	return text
}

func runConfig(args []string) {
	cmd := "show"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "show":
		configShow()
	case "redetect":
		detect.InvalidateCache()
		rep, _ := detect.RunCached(true)
		live, replay := 0, 0
		for _, s := range rep.Sources() {
			if s.Replay {
				replay++
			} else {
				live++
			}
		}
		fmt.Printf("detection cache refreshed: %d live + %d replay sources\n", live, replay)
	case "edit":
		configEdit()
	default:
		fmt.Fprintln(os.Stderr, "usage: wstat config [show|redetect|edit]")
		os.Exit(2)
	}
}

func configShow() {
	loaded := config.Load()
	fmt.Printf("# effective configuration\n")
	fmt.Printf("# user config:    %s\n", loaded.UserPath)
	if loaded.ProjectPath != "" {
		fmt.Printf("# project config: %s\n", loaded.ProjectPath)
	} else {
		fmt.Println("# project config: none (./wstat.toml)")
	}
	fmt.Printf("# detection:      %s\n", detect.CacheStatus())
	fmt.Println()
	fmt.Print(configTOML(loaded))
}

func configTOML(l *config.Loaded) string {
	var b strings.Builder
	origin := func(s string) string {
		if s == "" {
			return " (default)"
		}
		return " (" + s + ")"
	}
	b.WriteString("[source]\n")
	if len(l.Config.Source.Paths) > 0 {
		fmt.Fprintf(&b, "paths = [ #%s\n", origin(l.Origins.Paths))
		for _, p := range l.Config.Source.Paths {
			fmt.Fprintf(&b, "  %q,\n", p)
		}
		b.WriteString("]\n")
	} else {
		fmt.Fprintf(&b, "# paths unset — zero-config detection #%s\n", origin(""))
	}
	fmt.Fprintf(&b, "seed_lines = %d #%s\n", l.Config.Source.SeedLines, origin(l.Origins.SeedLine))
	if len(l.Config.Source.Vhost) > 0 {
		fmt.Fprintf(&b, "\n[source.vhost] #%s\n", origin(l.Origins.Vhost))
		for _, k := range sortedKeys(l.Config.Source.Vhost) {
			fmt.Fprintf(&b, "%q = %q\n", k, l.Config.Source.Vhost[k])
		}
	}
	fmt.Fprintf(&b, "\n[detect] #%s\n", origin(l.Origins.Detect))
	fmt.Fprintf(&b, "enabled = %v\ncache = %v\n", l.Config.Detect.Enabled, l.Config.Detect.Cache)
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func configEdit() {
	path := config.UserConfigPath()
	existed, err := config.SaveTemplate(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wstat config edit:", err)
		os.Exit(1)
	}
	if !existed {
		fmt.Println("created template:", path)
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wstat config edit:", err)
		os.Exit(1)
	}
	// Validate the result so mistakes surface immediately.
	if _, err := os.Stat(path); err != nil {
		return
	}
	if l := config.Load(); l.UserPath == path {
		fmt.Println("config saved:", path)
	}
}

func runInit(args []string) {
	force := len(args) > 0 && args[0] == "--redetect"
	rep, _ := detect.RunCached(force)
	live := 0
	items := make([]wizard.Item, 0)
	for _, s := range rep.Sources() {
		if s.Replay {
			continue // rotated history is covered by the path* globs
		}
		live++
		vhost := s.Vhost
		if v2 := pinFor(rep, s.Path); v2 != "" {
			vhost = v2
		}
		items = append(items, wizard.Item{Path: s.Path, Vhost: vhost, Live: true})
	}
	if live == 0 {
		fmt.Fprintln(os.Stderr, "wstat init: no live log sources detected — run `wstat doctor`")
		os.Exit(1)
	}

	selected := items
	m, err := tea.NewProgram(wizard.New(items)).Run()
	if err != nil {
		// No controlling terminal: select everything and write the config.
		fmt.Println("no interactive terminal:", err)
		fmt.Println("selecting all detected live sources")
	} else if w, ok := m.(wizard.Model); ok {
		if w.Aborted || !w.Saved {
			fmt.Println("aborted — nothing written")
			return
		}
		selected = w.Selected()
	}

	writeInitConfig(selected)
}

func pinFor(rep *detect.Report, path string) string {
	return rep.VhostMap()[path]
}

func writeInitConfig(items []wizard.Item) {
	var cfg config.Config
	cfg.Source.SeedLines = 1000
	cfg.Detect.Enabled = true
	cfg.Detect.Cache = true
	for _, it := range items {
		// Trailing-* globs cover rotated/gz history of the same vhost.
		cfg.Source.Paths = append(cfg.Source.Paths, it.Path+"*")
		if it.Vhost != "" {
			if cfg.Source.Vhost == nil {
				cfg.Source.Vhost = map[string]string{}
			}
			cfg.Source.Vhost[it.Path+"*"] = it.Vhost
		}
	}

	path := config.UserConfigPath()
	if _, err := os.Stat(path); err == nil {
		bak := path + ".bak"
		if err := os.Rename(path, bak); err == nil {
			fmt.Println("existing config backed up to", bak)
		}
	}
	if err := config.Save(path, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "wstat init:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n\n", path)
	fmt.Printf("[source]\npaths = [\n")
	for _, p := range cfg.Source.Paths {
		fmt.Printf("  %q,\n", p)
	}
	fmt.Printf("]\n\n[source.vhost]\n")
	for _, k := range sortedKeys(cfg.Source.Vhost) {
		fmt.Printf("%q = %q\n", k, cfg.Source.Vhost[k])
	}
	fmt.Printf("\n%d sources selected · `wstat` now uses this config; `wstat config edit` to adjust\n", len(items))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
