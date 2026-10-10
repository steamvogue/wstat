package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Writes are observed through a channel instead of reading a buffer while the
// animation goroutine owns it.
type loadingRecorder struct{ writes chan string }

func (r loadingRecorder) Write(p []byte) (int, error) {
	r.writes <- string(p)
	return len(p), nil
}

func TestLoadingAnimatesOneLineAndStopsBeforeDiagnostics(t *testing.T) {
	recorder := loadingRecorder{writes: make(chan string, 1024)}
	stop := animateLoading(recorder, "loading", 5*time.Millisecond)
	defer stop()
	var frames []string
	for len(frames) < 3 {
		select {
		case frame := <-recorder.writes:
			if !strings.Contains(frame, "loading") || strings.ContainsAny(frame, "\n") {
				t.Fatalf("loading must stay on one line: %q", frame)
			}
			frames = append(frames, frame)
		case <-time.After(3 * time.Second):
			t.Fatal("loading stopped animating")
		}
	}
	if frames[0] == frames[1] || frames[1] == frames[2] {
		t.Fatal("loading frame did not visibly change")
	}
	stop()
	stop() // Early diagnostics and deferred cleanup may both stop the loader.
	var rest strings.Builder
	for len(recorder.writes) > 0 {
		rest.WriteString(<-recorder.writes)
	}
	if !strings.HasSuffix(rest.String(), "\r\x1b[2K") {
		t.Fatalf("loader did not clear its final line: %q", rest.String())
	}
	_, _ = recorder.Write([]byte("wstat config: invalid configuration\n"))
	if line := <-recorder.writes; line != "wstat config: invalid configuration\n" {
		t.Fatalf("loader wrote after being stopped: %q", line)
	}
}

func TestLoadingIsSilentForRedirectedDiagnostics(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	stop := startLoading(file)
	stop()
	contents, err := os.ReadFile(file.Name())
	if err != nil || len(contents) != 0 {
		t.Fatalf("redirected stderr contains animation: %q, %v", contents, err)
	}
}

func TestNonDashboardCommandsDoNotShowLoading(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}, {"config", "show"}, {"-config", "missing.toml"}} {
		out, _ := cli(t, t.TempDir(), args...)
		if strings.Contains(out, "loading") || strings.Contains(out, "\x1b[") {
			t.Fatalf("%v has animation/control sequences: %q", args, out)
		}
	}
}
