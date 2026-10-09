package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"
)

// startProfiles has no side effects when both paths are empty. The returned
// cleanup must run after ingestion and polling have stopped.
func startProfiles(cpuPath, heapPath string, after ...time.Duration) (func() error, error) {
	var delay time.Duration
	if len(after) > 0 {
		delay = after[0]
	}
	if delay < 0 {
		return nil, fmt.Errorf("profile delay must be nonnegative")
	}
	if cpuPath != "" && heapPath != "" {
		a, _ := filepath.Abs(cpuPath)
		b, _ := filepath.Abs(heapPath)
		if a == b {
			return nil, fmt.Errorf("CPU and heap profile paths must differ")
		}
	}
	var cpu, heap *os.File
	var err error
	var startDone, startCancel chan struct{}
	var startErr error
	if heapPath != "" {
		heap, err = os.Create(heapPath)
		if err != nil {
			return nil, fmt.Errorf("heap profile: %w", err)
		}
	}
	if cpuPath != "" {
		cpu, err = os.Create(cpuPath)
		if err == nil && delay <= 0 {
			err = pprof.StartCPUProfile(cpu)
		}
		if err != nil {
			if cpu != nil {
				_ = cpu.Close()
			}
			if heap != nil {
				_ = heap.Close()
			}
			return nil, fmt.Errorf("CPU profile: %w", err)
		}
	}
	if cpu != nil && delay > 0 {
		startDone = make(chan struct{})
		startCancel = make(chan struct{})
		go func() {
			defer close(startDone)
			t := time.NewTimer(delay)
			defer t.Stop()
			select {
			case <-startCancel:
				startErr = fmt.Errorf("CPU profile stopped before delayed start")
				return
			case <-t.C:
			}
			startErr = pprof.StartCPUProfile(cpu)
		}()
	}
	return func() error {
		var errs []error
		if cpu != nil {
			if startDone != nil {
				close(startCancel)
				<-startDone
			}
			if startErr == nil {
				pprof.StopCPUProfile()
			} else {
				errs = append(errs, startErr)
			}
			errs = append(errs, cpu.Close())
		}
		if heap != nil {
			runtime.GC()
			errs = append(errs, pprof.WriteHeapProfile(heap), heap.Close())
		}
		return errors.Join(errs...)
	}, nil
}
