package fpm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/steamvogue/wstat/internal/filetail"
)

// PoolView is one pool's live state for the UI and doctor.
type PoolView struct {
	Pool
	// Tier 1 (status page), nil when unavailable.
	Status   *Status
	Disabled bool // pm.status_path not configured / request rejected
	Dead     bool // tier-1 and tier-2 both unavailable
	// Tier 2 (process stats).
	Workers int
	RSSKB   int64
	// Slowlog entries observed since startup.
	SlowSeen  int64
	SlowLag   bool
	SlowErr   string
	AccessErr string
	// Non-fatal probe errors.
	Err    string
	Access ServiceMetrics
}

// Poller periodically refreshes pool state (status via FastCGI, processes
// via /proc, slowlog tails). Safe for concurrent use.
type Poller struct {
	mu       sync.RWMutex
	pools    []Pool
	views    []PoolView
	stopCh   chan struct{}
	done     sync.WaitGroup
	slow     map[string]*slowCounter
	ctx      context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
	access   []ServiceMetrics
}

// NewPoller starts background polling of the given pools.
func NewPoller(pools []Pool, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Poller{pools: append([]Pool(nil), pools...), stopCh: make(chan struct{}), ctx: ctx, cancel: cancel, slow: map[string]*slowCounter{}}
	p.views = make([]PoolView, len(pools))
	p.access = make([]ServiceMetrics, len(pools))
	_, accessErrors := AccessParsers(pools)
	for i, pool := range pools {
		p.views[i] = PoolView{Pool: pool}

		p.views[i].AccessErr = accessErrors[pool.AccessLog]
		if pool.SlowLog != "" && p.slow[pool.SlowLog] == nil {
			p.slow[pool.SlowLog] = newSlowCounter(pool.SlowLog)
		}
	}
	p.done.Add(1)
	go p.loop(interval)
	return p
}

// Stop ends polling.
func (p *Poller) Stop() {
	p.stopOnce.Do(func() {
		p.cancel()
		close(p.stopCh)
		p.done.Wait()
		for _, c := range p.slow {
			_ = c.reader.Close()
		}
	})
}

func (p *Poller) loop(interval time.Duration) {
	defer p.done.Done()
	p.pollOnce()
	t := time.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.pollOnce()
			t.Reset(interval)
		}
	}
}

func (p *Poller) pollOnce() {
	stats := ReadProcStats()
	for _, c := range p.slow {
		c.poll()
	}
	ctx, cancel := context.WithTimeout(p.ctx, probeTimeout)
	defer cancel()
	jobs := make(chan int)
	var workers sync.WaitGroup
	for w := 0; w < min(4, len(p.pools)); w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				pool := p.pools[i]
				v := PoolView{Pool: pool, Workers: stats.ByPool[pool.Name], RSSKB: poolRSS(stats, pool)}
				if c := p.slow[pool.SlowLog]; c != nil {
					v.SlowSeen = c.counts[pool.Name]
					v.SlowLag = c.lag
					if c.err != nil {
						v.SlowErr = shortErr(c.err)
					}
				}
				if pool.StatusPath != "" && pool.Network != "" {
					if st, _, err := queryStatusContext(ctx, pool.Network, pool.Addr, pool.StatusPath, false); err == nil {
						v.Status = st
					} else if IsStatusDisabled(err) {
						v.Disabled = true
						v.Err = "pm.status_path not served (check pool config)"
					} else {
						v.Dead = true
						v.Err = shortErr(err)
					}
				} else if pool.Network == "" {
					v.Dead = true
					v.Err = "no listen address"
				} else {
					v.Disabled = true
					v.Err = "pm.status_path not configured"
				}
				p.mu.Lock()
				v.AccessErr = p.views[i].AccessErr
				p.views[i] = v
				p.mu.Unlock()
			}
		}()
	}
	for i := range p.pools {
		select {
		case jobs <- i:
		case <-p.ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

// A shared slowlog has one cursor and counters attributed by parsed pool name.
// Initialize at EOF: history is excluded from observed-since-startup metrics.
type slowCounter struct {
	reader *filetail.Reader
	counts map[string]int64
	lag    bool
	err    error
}

func newSlowCounter(path string) *slowCounter {
	f, err := filetail.Open(path)
	var offset int64
	var skip bool
	if err == nil {
		if fi, e := f.Stat(); e == nil {
			offset = fi.Size()
			var last [1]byte
			_, _ = f.ReadAt(last[:], offset-1)
			skip = offset > 0 && last[0] != '\n'
		}
	}
	return &slowCounter{reader: filetail.New(path, f, offset, skip), counts: map[string]int64{}}
}
func (c *slowCounter) poll() {
	lines, lag, err := c.reader.Read(256 * 1024)
	c.lag, c.err = lag, err
	for _, line := range lines {
		if e, ok := ParseSlowlogHead(line); ok && e.Pool != "" {
			if len(c.counts) < 1024 || c.counts[e.Pool] > 0 {
				c.counts[e.Pool]++
			} else {
				c.err = fmt.Errorf("slowlog pool attribution exceeds 1024 names")
			}
		}
	}
}

// Views returns a copy of the current pool states.
func (p *Poller) Views() []PoolView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PoolView, len(p.views))
	copy(out, p.views)
	for i := range out {
		out[i].Access = p.access[i]
		if out[i].Status != nil {
			st := *out[i].Status
			out[i].Status = &st
		}
	}
	return out
}

// ServiceMetrics describe access-log observations, independent of web totals
// and FPM's status counters (which include different time windows/probes).
type ServiceMetrics struct{ Requests, Seeded, Bad, DurationUs, DurationCount, MemoryBytes, MemoryCount int64 }

func (p *Poller) AddAccess(path string, r AccessRecord, seeded bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, pool := range p.pools {
		if pool.AccessLog != path || pool.Name != r.Pool {
			continue
		}
		a := &p.access[i]
		a.Requests++
		if seeded {
			a.Seeded++
		}
		if r.DurationKnown {
			a.DurationUs += r.LatencyUs
			a.DurationCount++
		}
		if r.MemoryKnown {
			a.MemoryBytes += r.MemoryBytes
			a.MemoryCount++
		}
	}
}
func (p *Poller) AddBadAccess(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, pool := range p.pools {
		if pool.AccessLog == path {
			p.access[i].Bad++
		}
	}
}

// AnyAlert reports a header-worthy condition (queue backlog or children
// exhaustion) and a short description.
func AnyAlert(views []PoolView) (bool, string) {
	for _, v := range views {
		if v.Status == nil {
			continue
		}
		if v.Status.ListenQueue > 0 {
			return true, fmt.Sprintf("fpm:%s queue=%d", v.Name, v.Status.ListenQueue)
		}
		if v.Status.MaxChildrenReached > 0 {
			return true, fmt.Sprintf("fpm:%s max-children", v.Name)
		}
	}
	return false, ""
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 {
		s = s[i+2:]
	}
	if len(s) > 60 {
		s = s[:57] + "…"
	}
	return s
}

func poolRSS(stats *ProcStats, pool Pool) int64 {
	// Per-pool RSS is not separable from /proc without matching pids to
	// pools; attribute total fpm RSS to single-pool hosts proportionally.
	if len(stats.ByPool) <= 1 {
		return stats.RSSKB
	}
	if w := stats.ByPool[pool.Name]; w > 0 && stats.Workers > 0 {
		return stats.RSSKB * int64(w) / int64(stats.Workers)
	}
	return 0
}
