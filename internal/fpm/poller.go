package fpm

import (
	"fmt"
	"strings"
	"sync"
	"time"
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
	SlowSeen int64
	// Non-fatal probe errors.
	Err string
}

// Poller periodically refreshes pool state (status via FastCGI, processes
// via /proc, slowlog tails). Safe for concurrent use.
type Poller struct {
	mu     sync.RWMutex
	pools  []Pool
	views  []PoolView
	stopCh chan struct{}
	done   sync.WaitGroup
	slow   map[string]*slowCounter
}

// NewPoller starts background polling of the given pools.
func NewPoller(pools []Pool, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	p := &Poller{pools: pools, stopCh: make(chan struct{})}
	p.views = make([]PoolView, len(pools))
	for i, pool := range pools {
		p.views[i] = PoolView{Pool: pool}
	}
	p.pollOnce() // populate immediately
	p.done.Add(1)
	go p.loop(interval)
	return p
}

// Stop ends polling.
func (p *Poller) Stop() {
	close(p.stopCh)
	p.done.Wait()
}

func (p *Poller) loop(interval time.Duration) {
	defer p.done.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.pollOnce()
		}
	}
}

func (p *Poller) pollOnce() {
	stats := ReadProcStats()
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, pool := range p.pools {
		v := PoolView{Pool: pool}
		v.Workers = stats.ByPool[pool.Name]
		v.RSSKB = poolRSS(stats, pool)
		v.SlowSeen = p.slowCount(pool)
		if pool.StatusPath != "" && pool.Network != "" {
			if st, _, err := QueryStatus(pool.Network, pool.Addr, pool.StatusPath); err == nil {
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
		p.views[i] = v
	}
}

func (p *Poller) slowCount(pool Pool) int64 {
	if pool.SlowLog == "" {
		return 0
	}
	if p.slow == nil {
		p.slow = map[string]*slowCounter{}
	}
	c, ok := p.slow[pool.SlowLog]
	if !ok {
		c = &slowCounter{base: countSlowLogLines(pool.SlowLog)}
		p.slow[pool.SlowLog] = c
	}
	return countSlowLogLines(pool.SlowLog) - c.base
}

// Views returns a copy of the current pool states.
func (p *Poller) Views() []PoolView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PoolView, len(p.views))
	copy(out, p.views)
	return out
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

type slowCounter struct{ base int64 }

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
