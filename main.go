// wstat: realtime per-vhost access-log monitor for Apache/nginx hosts.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steamvogue/wstat/internal/config"
	"github.com/steamvogue/wstat/internal/detect"
	"github.com/steamvogue/wstat/internal/fpm"
	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/store"
	"github.com/steamvogue/wstat/internal/ui"
	"github.com/steamvogue/wstat/internal/wizard"
)

// version is set at build time via -ldflags "-X main.version=…".
var version = "dev"

func main() { os.Exit(run()) }

func run() (code int) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "doctor":
			fmt.Print(runDoctor())
			return 0
		case "detect":
			runDetect(os.Args[2:])
			return 0
		case "config":
			runConfig(os.Args[2:])
			return 0
		case "init":
			runInit(os.Args[2:])
			return 0
		case "version":
			fmt.Printf("wstat %s\n", version)
			return 0
		}
	}

	seedN := flag.Int("n", 0, "lines to seed per file on startup; 0 skips history (overrides config)")
	configPath := flag.String("config", "", "explicit config file overlay")
	fpmEnabled := flag.Bool("fpm", true, "enable PHP-FPM discovery/probes/logs (overrides config)")
	redetect := flag.Bool("redetect", false, "force a fresh host detection, ignoring the cache")
	cpuPath := flag.String("cpuprofile", "", "write local CPU profile to this file (opt-in)")
	profileAfter := flag.Duration("profile-after", 0, "delay CPU profiling to exclude startup (e.g. 3s)")
	heapPath := flag.String("heapprofile", "", "write retained heap profile on shutdown (opt-in)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("wstat %s\n", version)
		return 0
	}

	stopLoading := startLoading(os.Stderr)
	defer stopLoading()

	cleanup, err := startProfiles(*cpuPath, *heapPath, *profileAfter)
	if err != nil {
		stopLoading()
		fmt.Fprintln(os.Stderr, "wstat:", err)
		return 1
	}
	var profileStore *store.Store
	defer func() {
		if err := cleanup(); err != nil {
			fmt.Fprintln(os.Stderr, "wstat:", err)
			code = 1
		}
		runtime.KeepAlive(profileStore)
	}()
	loaded := config.LoadWithPath(*configPath)
	if err := loaded.Err(); err != nil {
		stopLoading()
		fmt.Fprintln(os.Stderr, "wstat config:", err)
		return 1
	}
	cfg := loaded.Config
	seed := cfg.Source.SeedLines
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "n":
			seed = *seedN
		case "fpm":
			cfg.FPM.Enabled = *fpmEnabled
		}
	})
	if seed < 0 || seed > 100000 {
		stopLoading()
		fmt.Fprintln(os.Stderr, "wstat: -n must be between 0 and 100000")
		return 2
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
		rep, _ := detect.RunCachedWithOptions(*redetect, cfg.Detect.Cache)
		sources = rep.Sources()
		vhostMap = rep.VhostMap()
		// Config vhost pins override detection attribution.
		for k, v := range cfg.Source.Vhost {
			vhostMap[k] = v
		}
	default:
		sources = logsrc.Discover(nil, cfg.Source.Vhost)
	}

	if vhostMap == nil {
		vhostMap = map[string]string{}
	}
	for k, v := range cfg.Source.Vhost {
		vhostMap[k] = v
	}
	for path := range vhostMap {
		if v := logsrc.MatchVhostPin(cfg.Source.Vhost, path); v != "" {
			vhostMap[path] = v
		}
	}
	for i := range sources {
		if v := logsrc.MatchVhostPin(cfg.Source.Vhost, sources[i].Path); v != "" {
			sources[i].Vhost = v
		}
	}
	if len(globs) == 0 && len(cfg.Source.Paths) == 0 {
		rescanGlobs = append(append([]string(nil), rescanGlobs...), pinPaths(cfg.Source.Vhost)...)
	}
	existing := map[string]bool{}
	for _, src := range sources {
		existing[src.Path] = true
	}
	var pinned []logsrc.Source
	if len(globs) == 0 && len(cfg.Source.Paths) == 0 && len(cfg.Source.Vhost) > 0 {
		pinned = logsrc.Discover(pinPaths(cfg.Source.Vhost), vhostMap)
	}
	for _, src := range pinned {
		if len(cfg.Source.Vhost) > 0 && !existing[src.Path] {
			sources = append(sources, src)
			existing[src.Path] = true
		}
	}
	if len(sources) == 0 {
		stopLoading()
		fmt.Fprintln(os.Stderr, "wstat: no access logs found")
		fmt.Fprintf(os.Stderr, "  searched: %s\n", strings.Join(rescanGlobs, " "))
		fmt.Fprintln(os.Stderr, "run `wstat doctor` to see what was detected on this host")
		fmt.Fprintln(os.Stderr, "usage: wstat [glob ...]   e.g. wstat '/var/log/apache2/*-access.log'")
		return 1
	}

	// Each source has a single metric owner. FPM events stay in Services.
	var fpmPools []fpm.Pool
	if cfg.FPM.Enabled {
		fpmPools = fpm.DiscoverPools()
	}
	router := newIngestionRouter()
	var serviceDiagnostics map[string]string
	router.services, serviceDiagnostics = fpm.AccessParsers(fpmPools)
	if len(serviceDiagnostics) > 0 {
		stopLoading()
	}
	for _, path := range sortedKeys(serviceDiagnostics) {
		fmt.Fprintf(os.Stderr, "wstat: FPM %s: %s\n", path, serviceDiagnostics[path])
	}
	for _, pool := range fpmPools {
		if pool.AccessLog == "" {
			continue
		}
		service := logsrc.Source{Path: pool.AccessLog, Vhost: pool.Name, Kind: logsrc.FPM, Format: pool.AccessFormat}
		found := false
		for i := range sources {
			if sources[i].Path == pool.AccessLog {
				sources[i] = service
				found = true
			}
		}
		if !found {
			sources = append(sources, service)
		}

	}

	var fpmViews func() []fpm.PoolView
	if len(fpmPools) > 0 {
		router.poller = fpm.NewPoller(fpmPools, 2*time.Second)
		defer router.poller.Stop()
		fpmViews = router.poller.Views
	}
	tailer := logsrc.Start(sources, rescanGlobs, seed, vhostMap)
	st := store.New()
	profileStore = st
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for line := range tailer.Ch {
			router.ingest(st, line)
		}
	}()
	defer func() { tailer.Stop(); <-drained }()

	stopLoading()
	if _, err := tea.NewProgram(ui.New(st, tailer, fpmViews)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wstat:", err)
		return 1
	}
	_, _ = fmt.Fprintln(os.Stdout) // Restore a clean line for the caller's next prompt.
	return 0
}

func runDetect(args []string) {
	fs := flag.NewFlagSet("detect", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit the raw detection report as JSON")
	force := fs.Bool("redetect", false, "ignore the detection cache")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "wstat detect:", err)
		os.Exit(2)
	}
	cfg := loadCommandConfig()
	rep, _ := detect.RunCachedWithOptions(*force, cfg.Detect.Cache)
	if *asJSON {
		fmt.Println(rep.JSON())
		return
	}
	fmt.Print(rep.Doctor())
}

func runDoctor() string {
	cfg := loadCommandConfig()
	text := detect.Run().Doctor()
	if cfg.FPM.Enabled {
		text += fpm.DoctorText(fpm.DiscoverPools())
	}
	text += "detection:   " + cacheStatus(cfg) + "\n"
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
		cfg := loadCommandConfig()
		rep, _ := detect.RunCachedWithOptions(true, cfg.Detect.Cache)
		live, replay := 0, 0
		for _, s := range rep.Sources() {
			if s.Replay {
				replay++
			} else {
				live++
			}
		}
		label := "detection cache refreshed"
		if !cfg.Detect.Cache {
			label = "detection refreshed (cache disabled)"
		}
		fmt.Printf("%s: %d live + %d replay sources\n", label, live, replay)
	case "edit":
		configEdit()
	default:
		fmt.Fprintln(os.Stderr, "usage: wstat config [show|redetect|edit]")
		os.Exit(2)
	}
}

func configShow() {
	loaded := config.Load()
	if err := loaded.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "wstat config:", err)
		os.Exit(1)
	}
	fmt.Printf("# effective configuration\n")
	fmt.Printf("# user config:    %s\n", loaded.UserPath)
	if loaded.ProjectPath != "" {
		fmt.Printf("# project config: %s\n", loaded.ProjectPath)
	} else {
		fmt.Println("# project config: none (./wstat.toml)")
	}
	fmt.Printf("# detection:      %s\n", cacheStatus(loaded.Config))
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
	fmt.Fprintf(&b, "\n[fpm] #%s\nenabled = %v\n", origin(l.Origins.FPM), l.Config.FPM.Enabled)
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
		fmt.Fprintln(os.Stderr, "wstat config edit:", err)
		os.Exit(1)
	}
	if err := config.ValidateFile(path); err != nil {
		fmt.Fprintln(os.Stderr, "wstat config edit:", err)
		os.Exit(1)
	}
	fmt.Println("config saved:", path)
}

func runInit(args []string) {
	force := len(args) > 0 && args[0] == "--redetect"
	cfg := loadCommandConfig()
	rep, _ := detect.RunCachedWithOptions(force, cfg.Detect.Cache)
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
	cfg := config.Default()
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

func pinPaths(pins map[string]string) []string { return sortedKeys(pins) }

func loadCommandConfig() config.Config {
	l := config.Load()
	if err := l.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "wstat config:", err)
		os.Exit(1)
	}
	return l.Config
}
func cacheStatus(c config.Config) string {
	if !c.Detect.Cache {
		return "disabled (no cache reads/writes)"
	}
	return detect.CacheStatus()
}
