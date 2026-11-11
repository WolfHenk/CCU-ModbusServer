// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/poller/poller.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.2.2
//
// Copyright (c) 2026 Wolfram Henkel.
//
// Lizenz:       CCU-ModbusServer-Lizenz für kostenfreie Nutzung und
//               Weitergabe, Version 1.1.
//
// Private und gewerbliche Nutzung sind erlaubt.
// Die Software und Änderungen dürfen nur kostenlos unter
// derselben Lizenz und mit vollständigem Quellcode weitergegeben werden.
//
// Vollständiger Lizenztext: LICENSE
// -----------------------------------------------------------------------------

// Package poller reads the values of the exported objects from a data
// source in regular, non-overlapping cycles. The cycle interval adapts to
// the cost of a read so that the data source (ReGa) is never busy with
// this add-on for more than a set share of the time.
package poller

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

// Source is the part of a data source the poller needs. *rega.Client
// implements it.
type Source interface {
	ReadValues(ctx context.Context, ids []int) (map[int]rega.Reading, error)
	// Ping checks that the source answers; used when nothing is exported.
	Ping(ctx context.Context) error
}

// Config controls timing. Zero values take the defaults.
type Config struct {
	Interval     time.Duration // wanted cycle time, default 1 s
	MaxShare     float64       // max share of time spent reading, default 0.10
	Timeout      time.Duration // per read, default 5 s
	Batch        int           // max IDs per read, default 500
	IdleInterval time.Duration // ping interval without items, default 10 s
	BackoffMin   time.Duration // first retry after an error, default 2 s
	BackoffMax   time.Duration // longest retry delay, default 30 s
}

func (c *Config) defaults() {
	if c.Interval <= 0 {
		c.Interval = time.Second
	}
	if c.MaxShare <= 0 || c.MaxShare > 1 {
		c.MaxShare = 0.10
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.Batch <= 0 {
		c.Batch = 500
	}
	if c.IdleInterval <= 0 {
		c.IdleInterval = 10 * time.Second
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = 2 * time.Second
	}
	if c.BackoffMax < c.BackoffMin {
		c.BackoffMax = 30 * time.Second
	}
}

// Health describes the last cycles.
type Health struct {
	OK        bool          `json:"ok"`            // last cycle succeeded
	LastOK    time.Time     `json:"last_ok"`       // end of the last successful cycle
	LastError string        `json:"last_error"`    // empty after success
	Items     int           `json:"items"`         // distinct IDs polled
	Duration  time.Duration `json:"duration_ns"`   // duration of the last cycle
	NextDelay time.Duration `json:"next_delay_ns"` // pause before the next cycle
	Cycles    uint64        `json:"cycles"`
	Failures  uint64        `json:"failures"`
}

// Poller runs the read cycles.
type Poller struct {
	cfg Config
	src Source

	mu      sync.Mutex
	items   []int
	gen     uint64 // incremented by every SetItems
	values  map[int]rega.Reading
	health  Health
	changed chan struct{}

	// OnCycle, if set, is called after every cycle with the complete
	// result of a successful cycle (nil after a failed one). It runs in
	// the poller goroutine.
	OnCycle func(values map[int]rega.Reading, h Health)
}

// New creates a poller.
func New(src Source, cfg Config) *Poller {
	cfg.defaults()
	return &Poller{cfg: cfg, src: src, values: map[int]rega.Reading{}, changed: make(chan struct{}, 1)}
}

// SetItems replaces the polled IDs (duplicates and IDs <= 0 removed).
// Unless the source is failing, the next cycle starts immediately.
func (p *Poller) SetItems(ids []int) {
	seen := map[int]bool{}
	var list []int
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			list = append(list, id)
		}
	}
	sort.Ints(list)
	p.mu.Lock()
	p.items = list
	p.gen++
	// Values of IDs that are no longer polled are dropped at once, so a
	// removed or re-mapped object never shows an old reading.
	for id := range p.values {
		if !seen[id] {
			delete(p.values, id)
		}
	}
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// Values returns a copy of the last successful readings and the health.
func (p *Poller) Values() (map[int]rega.Reading, Health) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[int]rega.Reading, len(p.values))
	for k, v := range p.values {
		out[k] = v
	}
	return out, p.health
}

// Health returns the current health.
func (p *Poller) Health() Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health
}

// delay computes the pause after a successful cycle of duration d: at
// least the configured interval, and long enough that reading takes at
// most MaxShare of the time.
func (c Config) delay(d time.Duration, items int) time.Duration {
	base := c.Interval
	if items == 0 {
		base = c.IdleInterval
	}
	min := time.Duration(float64(d)/c.MaxShare) - d
	if min > base {
		return min
	}
	return base
}

// cycle reads all items in batches. Only a complete, successful read of
// all batches is returned; any error discards the whole cycle.
func (p *Poller) cycle(ctx context.Context, items []int) (map[int]rega.Reading, error) {
	if len(items) == 0 {
		cctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
		defer cancel()
		return map[int]rega.Reading{}, p.src.Ping(cctx)
	}
	out := make(map[int]rega.Reading, len(items))
	for i := 0; i < len(items); i += p.cfg.Batch {
		j := i + p.cfg.Batch
		if j > len(items) {
			j = len(items)
		}
		cctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
		r, err := p.src.ReadValues(cctx, items[i:j])
		cancel()
		if err != nil {
			return nil, err
		}
		for k, v := range r {
			out[k] = v
		}
	}
	return out, nil
}

// Run polls until ctx is cancelled. Cycles never overlap.
func (p *Poller) Run(ctx context.Context) {
	backoff := time.Duration(0)
	for {
		// A change signalled before this point is covered by this cycle.
		select {
		case <-p.changed:
		default:
		}
		p.mu.Lock()
		items := append([]int(nil), p.items...)
		gen := p.gen
		p.mu.Unlock()

		start := time.Now()
		vals, err := p.cycle(ctx, items)
		d := time.Since(start)
		if ctx.Err() != nil {
			return
		}

		var wait time.Duration
		p.mu.Lock()
		if err == nil && gen != p.gen {
			// The selection changed while this cycle was reading: its
			// result belongs to the old selection and is discarded (not
			// even counted), so a removed ID can never come back. The new
			// selection is read right away.
			p.mu.Unlock()
			continue
		}
		p.health.Cycles++
		p.health.Duration = d
		p.health.Items = len(items)
		if err != nil {
			p.health.OK = false
			p.health.Failures++
			p.health.LastError = err.Error()
			if backoff == 0 {
				backoff = p.cfg.BackoffMin
			} else if backoff *= 2; backoff > p.cfg.BackoffMax {
				backoff = p.cfg.BackoffMax
			}
			wait = backoff
			vals = nil
		} else {
			backoff = 0
			p.health.OK = true
			p.health.LastOK = time.Now()
			p.health.LastError = ""
			p.values = vals
			wait = p.cfg.delay(d, len(items))
		}
		p.health.NextDelay = wait
		h := p.health
		p.mu.Unlock()
		if p.OnCycle != nil {
			p.OnCycle(vals, h)
		}

		// While the source is failing, item changes do not shorten the
		// backoff; they are picked up by the next regular cycle.
		changed := p.changed
		if err != nil {
			changed = nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-changed:
			t.Stop()
		case <-t.C:
		}
	}
}
