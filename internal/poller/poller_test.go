// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/poller/poller_test.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.2.0
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

package poller

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

type fakeSource struct {
	blockErr error         // returned by the blocked read
	block    chan struct{} // if set, the first read waits until it is closed
	started  chan struct{} // closed when the blocking read has started
	mu       sync.Mutex
	calls    [][]int
	pings    int
	fail     error
	failOnce map[int]bool // fail the batch that starts with this id
	sleep    time.Duration
	active   atomic.Int32
	overlap  atomic.Bool
	gen      int
}

func (f *fakeSource) ReadValues(ctx context.Context, ids []int) (map[int]rega.Reading, error) {
	if f.active.Add(1) > 1 {
		f.overlap.Store(true)
	}
	defer f.active.Add(-1)
	f.mu.Lock()
	block, started, blockErr := f.block, f.started, f.blockErr
	f.block = nil
	f.mu.Unlock()
	if block != nil {
		close(started)
		<-block
		if blockErr != nil {
			return nil, blockErr
		}
	}
	if f.sleep > 0 {
		time.Sleep(f.sleep)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]int(nil), ids...))
	if f.fail != nil {
		return nil, f.fail
	}
	if f.failOnce[ids[0]] {
		delete(f.failOnce, ids[0])
		return nil, errors.New("batch failed")
	}
	f.gen++
	out := map[int]rega.Reading{}
	for _, id := range ids {
		out[id] = rega.Reading{Found: true, Value: strconv.Itoa(f.gen), Timestamp: 1}
	}
	return out, nil
}

func (f *fakeSource) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	return f.fail
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDelayThrottle(t *testing.T) {
	c := Config{}
	c.defaults()
	if d := c.delay(10*time.Millisecond, 5); d != time.Second {
		t.Errorf("cheap read: %v", d)
	}
	// 200 ms read at 10 % share -> 1.8 s pause
	if d := c.delay(200*time.Millisecond, 5); d != 1800*time.Millisecond {
		t.Errorf("expensive read: %v", d)
	}
	if d := c.delay(time.Millisecond, 0); d != 10*time.Second {
		t.Errorf("idle: %v", d)
	}
}

func TestBatchingDedupAndValues(t *testing.T) {
	src := &fakeSource{}
	p := New(src, Config{Interval: time.Hour, Batch: 3})
	p.SetItems([]int{5, 1, 2, 2, 0, -1, 4, 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, func() bool { return p.Health().Cycles >= 1 })
	src.mu.Lock()
	calls := src.calls
	src.mu.Unlock()
	if len(calls) != 2 || len(calls[0]) != 3 || len(calls[1]) != 2 || calls[0][0] != 1 || calls[1][1] != 5 {
		t.Fatalf("batches: %v", calls)
	}
	v, h := p.Values()
	if !h.OK || h.Items != 5 || len(v) != 5 || !v[4].Found {
		t.Fatalf("values %v health %+v", v, h)
	}
}

func TestFailedBatchDiscardsWholeCycleAndKeepsOldValues(t *testing.T) {
	src := &fakeSource{}
	p := New(src, Config{Interval: 20 * time.Millisecond, Batch: 2, BackoffMin: 20 * time.Millisecond})
	p.SetItems([]int{1, 2, 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cycles []map[int]rega.Reading
	var mu sync.Mutex
	p.OnCycle = func(v map[int]rega.Reading, h Health) {
		mu.Lock()
		cycles = append(cycles, v)
		mu.Unlock()
	}
	go p.Run(ctx)
	waitFor(t, func() bool { return p.Health().Cycles >= 1 })
	first, _ := p.Values()
	src.mu.Lock()
	src.failOnce = map[int]bool{3: true} // second batch of the next cycle fails
	src.mu.Unlock()
	waitFor(t, func() bool { return p.Health().Failures >= 1 })
	v, h := p.Values()
	if h.OK || h.LastError == "" {
		t.Fatalf("health %+v", h)
	}
	if v[1].Value != first[1].Value || v[3].Value != first[3].Value {
		t.Fatalf("partial cycle leaked: first %v now %v", first, v)
	}
	waitFor(t, func() bool { return p.Health().OK })
	mu.Lock()
	defer mu.Unlock()
	sawNil := false
	for _, c := range cycles {
		if c == nil {
			sawNil = true
		}
	}
	if !sawNil {
		t.Error("OnCycle not called with nil after failure")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	src := &fakeSource{fail: errors.New("down")}
	p := New(src, Config{BackoffMin: 10 * time.Millisecond, BackoffMax: 40 * time.Millisecond})
	p.SetItems([]int{1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var delays []time.Duration
	var mu sync.Mutex
	p.OnCycle = func(_ map[int]rega.Reading, h Health) {
		mu.Lock()
		delays = append(delays, h.NextDelay)
		mu.Unlock()
	}
	go p.Run(ctx)
	waitFor(t, func() bool { return p.Health().Failures >= 5 })
	// item changes must not bypass the backoff
	before := p.Health().Cycles
	for i := 0; i < 20; i++ {
		p.SetItems([]int{1, 2})
	}
	time.Sleep(15 * time.Millisecond)
	if after := p.Health().Cycles; after > before+1 {
		t.Errorf("SetItems bypassed backoff: %d -> %d cycles", before, after)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{10, 20, 40, 40, 40}
	for i, w := range want {
		if delays[i] != w*time.Millisecond {
			t.Fatalf("delays %v", delays[:5])
		}
	}
}

func TestIdlePingsAndNoOverlap(t *testing.T) {
	src := &fakeSource{sleep: 5 * time.Millisecond}
	p := New(src, Config{IdleInterval: 10 * time.Millisecond, Interval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, func() bool { src.mu.Lock(); defer src.mu.Unlock(); return src.pings >= 2 })
	if !p.Health().OK {
		t.Fatal("idle ping not healthy")
	}
	p.SetItems([]int{1, 2, 3})
	waitFor(t, func() bool { return p.Health().Cycles >= 10 })
	if src.overlap.Load() {
		t.Fatal("overlapping reads")
	}
}

func TestStopsOnCancel(t *testing.T) {
	src := &fakeSource{}
	p := New(src, Config{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	waitFor(t, func() bool { return p.Health().Cycles >= 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}

func TestSetItemsDropsRemovedValues(t *testing.T) {
	src := &fakeSource{}
	p := New(src, Config{Interval: time.Hour})
	p.SetItems([]int{1, 2, 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, func() bool { return p.Health().Cycles >= 1 })
	src.mu.Lock()
	src.fail = errors.New("down") // next cycle fails: nothing new arrives
	src.mu.Unlock()
	p.SetItems([]int{2, 4})
	v, _ := p.Values()
	if _, ok := v[1]; ok {
		t.Error("removed id 1 still has a value")
	}
	if _, ok := v[4]; ok {
		t.Error("new id 4 has a value before it was read")
	}
	if !v[2].Found {
		t.Error("kept id 2 lost its value")
	}
}

// Regression (ChatGPT review 2b): a cycle that was reading while the
// selection changed must not write its result back.
func TestCycleDuringSetItemsIsDiscarded(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	src := &fakeSource{block: block, started: started}
	p := New(src, Config{Interval: time.Hour})
	p.SetItems([]int{1, 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var seen []map[int]rega.Reading
	p.OnCycle = func(v map[int]rega.Reading, h Health) {
		mu.Lock()
		seen = append(seen, v)
		mu.Unlock()
	}
	go p.Run(ctx)
	<-started // first cycle for [1 2] is blocked inside ReadValues
	p.SetItems([]int{2, 3})
	if v, _ := p.Values(); len(v) != 0 {
		t.Fatalf("values before any cycle: %v", v)
	}
	close(block) // let the old cycle finish
	waitFor(t, func() bool { return p.Health().Cycles >= 1 })
	v, _ := p.Values()
	if _, ok := v[1]; ok {
		t.Fatal("removed id 1 came back from the old cycle")
	}
	if !v[3].Found || !v[2].Found {
		t.Fatalf("new selection not read: %v", v)
	}
	src.mu.Lock()
	calls := src.calls
	src.mu.Unlock()
	if len(calls) != 2 || len(calls[1]) != 2 || calls[1][0] != 2 || calls[1][1] != 3 {
		t.Fatalf("reads: %v", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range seen {
		if _, ok := c[1]; ok {
			t.Fatal("OnCycle saw the discarded result")
		}
	}
}

// Regression (ChatGPT review 0.2.3, A): a FAILED read of an old selection
// must not count as failure or cause backoff for the new selection.
func TestFailedCycleDuringSetItemsIsDiscarded(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	src := &fakeSource{block: block, started: started, blockErr: errors.New("old read failed")}
	p := New(src, Config{Interval: time.Hour, BackoffMin: time.Hour})
	p.SetItems([]int{1, 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	<-started
	p.SetItems([]int{2, 3})
	close(block)
	waitFor(t, func() bool { return p.Health().Cycles >= 1 })
	h := p.Health()
	if h.Failures != 0 || !h.OK || h.Cycles != 1 {
		t.Fatalf("old failure leaked into health: %+v", h)
	}
	if v, _ := p.Values(); !v[3].Found {
		t.Fatalf("new selection not read: %v", v)
	}
}

func TestReadAtIsCycleStart(t *testing.T) {
	src := &fakeSource{sleep: 30 * time.Millisecond}
	p := New(src, Config{Interval: time.Hour})
	p.SetItems([]int{1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := time.Now()
	go p.Run(ctx)
	waitFor(t, func() bool { return p.Health().Cycles >= 1 })
	at := p.ReadAt(1)
	if at.Before(before) || !at.Before(p.Health().LastOK.Add(-20*time.Millisecond)) {
		t.Fatalf("readAt %v not the cycle start (lastOK %v)", at, p.Health().LastOK)
	}
	p.SetItems([]int{2})
	if !p.ReadAt(1).IsZero() {
		t.Fatal("readAt kept for removed id")
	}
}
