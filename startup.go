package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
)

// Startup work happens before Bubble Tea owns the terminal. Keep progress on
// stderr so command output and redirected diagnostics remain usable.
func startLoading(out *os.File) func() {
	if !term.IsTerminal(out.Fd()) || os.Getenv("TERM") == "dumb" {
		return func() {}
	}
	message := "wstat: loading config / detection"
	if width, _, err := term.GetSize(out.Fd()); err == nil && width > 0 && width < len(message)+2 {
		message = "loading"
	}
	return animateLoading(out, message, 120*time.Millisecond)
}

// The returned stop waits for the writer to finish and clears its single line,
// so diagnostics and the dashboard cannot race a final animation frame.
func animateLoading(out io.Writer, message string, interval time.Duration) func() {
	frames := "|/-\\"
	draw := func(frame int) {
		_, _ = fmt.Fprintf(out, "\r\x1b[2K%s %c", message, frames[frame%len(frames)])
	}
	draw(0)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for frame := 1; ; {
			select {
			case <-stop:
				_, _ = fmt.Fprint(out, "\r\x1b[2K")
				return
			case <-ticker.C:
				draw(frame)
				frame++
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
