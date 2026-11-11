// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/engine/write_test.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.3.0
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

package engine

import (
	"context"
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/mapping"
	"github.com/WolfHenk/CCU-ModbusServer/internal/modbus"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

type setCall struct {
	id int
	t  rega.Type
	v  float64
}

type fakeSetter struct {
	mu    sync.Mutex
	calls []setCall
	err   map[int]error
}

func (f *fakeSetter) SetValue(ctx context.Context, id int, t rega.Type, v float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, setCall{id, t, v})
	return f.err[id]
}

func (f *fakeSetter) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Writable test setup: setpoint (FLOAT 4.5..30.5, id 21), mode (ENUM 3
// entries, id 22), switch (BOOL, id 23), boost (ACTION, id 24).
func writeCatalog() *rega.Catalog {
	return &rega.Catalog{Devices: []rega.Device{{Interface: "HmIP-RF", Address: "T1", Channels: []rega.Channel{
		{Address: "T1:0", Points: []rega.Point{{ReGaID: 20, ID: "HmIP-RF.T1:0.UNREACH", Param: "UNREACH", Type: rega.Bool, Readable: true}}},
		{Address: "T1:1", Points: []rega.Point{
			{ReGaID: 21, ID: "HmIP-RF.T1:1.SET_POINT_TEMPERATURE", Type: rega.Float, Min: fp(4.5), Max: fp(30.5), Readable: true, Writable: true},
			{ReGaID: 22, ID: "HmIP-RF.T1:1.SET_POINT_MODE", Type: rega.Enum, ValueList: []string{"AUTO", "MANU", "PARTY"}, Readable: true, Writable: true},
			{ReGaID: 23, ID: "HmIP-RF.T1:1.STATE", Type: rega.Bool, Readable: true, Writable: true},
			{ReGaID: 24, ID: "HmIP-RF.T1:1.BOOST_MODE", Type: rega.Action, Writable: true},
			{ReGaID: 25, ID: "HmIP-RF.T1:1.ACTUAL_TEMPERATURE", Type: rega.Float, Min: fp(-40), Max: fp(80), Readable: true},
		}},
	}}}}
}

func writeMapping(t *testing.T) *mapping.File {
	t.Helper()
	m := mapping.New(4)
	for _, e := range []mapping.Entry{
		{Key: "dp:HmIP-RF.T1:1.SET_POINT_TEMPERATURE", Kind: "dp", Type: rega.Float, Format: mapping.Float32, Writable: true}, // HR 0-1
		{Key: "dp:HmIP-RF.T1:1.SET_POINT_MODE", Kind: "dp", Type: rega.Enum, Format: mapping.Uint16, Writable: true},          // HR 2
		{Key: "dp:HmIP-RF.T1:1.STATE", Kind: "dp", Type: rega.Bool, Format: mapping.Bit, Writable: true},                      // coil 0
		{Key: "dp:HmIP-RF.T1:1.BOOST_MODE", Kind: "dp", Type: rega.Action, Format: mapping.Bit, Writable: true},               // coil 1
		{Key: "dp:HmIP-RF.T1:1.ACTUAL_TEMPERATURE", Kind: "dp", Type: rega.Float, Format: mapping.Float32},                    // IR 4-5
	} {
		if _, err := m.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

var allowed = &net.TCPAddr{IP: net.ParseIP("192.168.3.50"), Port: 40000}
var stranger = &net.TCPAddr{IP: net.ParseIP("10.0.0.9"), Port: 40000}

func f32(v float32) []uint16 {
	b := math.Float32bits(v)
	return []uint16{uint16(b >> 16), uint16(b)}
}

func setupWrite(t *testing.T) (*Engine, *fakePoller, *fakeSetter) {
	t.Helper()
	pol := &fakePoller{}
	e := newEngine(t, writeMapping(t), pol, nil)
	e.SetCatalog(writeCatalog())
	pol.set(map[int]rega.Reading{
		20: {Found: true, Value: "false", Timestamp: 1},
		21: {Found: true, Value: "20.000000", Timestamp: 1},
		22: {Found: true, Value: "0", Timestamp: 1},
		23: {Found: true, Value: "false", Timestamp: 1},
		24: {Found: true, Value: "", Timestamp: 0},
		25: {Found: true, Value: "19.500000", Timestamp: 1},
	}, time.Now())
	allow, err := ParseAllow([]string{"192.168.3.0/24", "fd00::1"})
	if err != nil {
		t.Fatal(err)
	}
	set := &fakeSetter{err: map[int]error{}}
	e.SetWriter(set, WriteConfig{Allow: allow, MinInterval: time.Hour})
	return e, pol, set
}

func wantEx(t *testing.T, err error, ex modbus.Exception) {
	t.Helper()
	var got modbus.Exception
	if !errors.As(err, &got) || got != ex {
		t.Fatalf("got %v, want exception %02x", err, byte(ex))
	}
}

func TestWriteHappyPath(t *testing.T) {
	e, _, set := setupWrite(t)
	if err := e.WriteRegisters(allowed, 0, f32(21.5)); err != nil {
		t.Fatal(err)
	}
	if err := e.WriteRegisters(allowed, 2, []uint16{2}); err != nil {
		t.Fatal(err)
	}
	if err := e.WriteCoils(allowed, 0, []bool{true}); err != nil {
		t.Fatal(err)
	}
	if err := e.WriteCoils(allowed, 1, []bool{true}); err != nil {
		t.Fatal(err)
	}
	want := []setCall{{21, rega.Float, 21.5}, {22, rega.Enum, 2}, {23, rega.Bool, 1}, {24, rega.Action, 1}}
	if len(set.calls) != len(want) {
		t.Fatalf("calls %+v", set.calls)
	}
	for i := range want {
		if set.calls[i] != want[i] {
			t.Errorf("call %d: %+v want %+v", i, set.calls[i], want[i])
		}
	}
	log := e.WriteLog()
	if len(log) != 4 || log[0].Client != "192.168.3.50" || log[0].Result[:2] != "ok" {
		t.Fatalf("log %+v", log)
	}
}

func TestWriteRejections(t *testing.T) {
	e, _, set := setupWrite(t)
	wantEx(t, e.WriteRegisters(stranger, 0, f32(21.5)), modbus.ExIllegalFunction) // not allowed
	wantEx(t, e.WriteRegisters(nil, 0, f32(21.5)), modbus.ExIllegalFunction)      // unknown client
	wantEx(t, e.WriteRegisters(allowed, 0, f32(31)), modbus.ExIllegalDataValue)   // above MAX
	wantEx(t, e.WriteRegisters(allowed, 0, f32(4)), modbus.ExIllegalDataValue)    // below MIN
	wantEx(t, e.WriteRegisters(allowed, 0, mapping.NaNWords("ABCD")), modbus.ExIllegalDataValue)
	wantEx(t, e.WriteRegisters(allowed, 2, []uint16{3}), modbus.ExIllegalDataValue)        // enum index
	wantEx(t, e.WriteRegisters(allowed, 1, []uint16{0}), modbus.ExIllegalDataAddress)      // second half
	wantEx(t, e.WriteRegisters(allowed, 0, []uint16{0x41AC}), modbus.ExIllegalDataAddress) // first half only
	// multi-write: the second value is invalid -> nothing is sent at all
	wantEx(t, e.WriteRegisters(allowed, 0, append(f32(21.5), 7)), modbus.ExIllegalDataValue)
	if n := set.n(); n != 0 {
		t.Fatalf("%d SetValue calls despite rejections", n)
	}
	if l := e.WriteLog(); len(l) != 9 {
		t.Fatalf("log length %d", len(l))
	}
}

func TestWriteSameValueAndRateLimit(t *testing.T) {
	e, _, set := setupWrite(t)
	// equal to the current CCU value: acknowledged, not forwarded
	if err := e.WriteRegisters(allowed, 0, f32(20)); err != nil {
		t.Fatal(err)
	}
	if set.n() != 0 {
		t.Fatal("unchanged value forwarded")
	}
	if err := e.WriteRegisters(allowed, 0, f32(21)); err != nil {
		t.Fatal(err)
	}
	// second change within MinInterval -> busy, nothing sent
	wantEx(t, e.WriteRegisters(allowed, 0, f32(22)), modbus.ExServerDeviceBusy)
	if set.n() != 1 {
		t.Fatalf("calls %d", set.n())
	}
	// actions are never "unchanged", but rate limited
	if err := e.WriteCoils(allowed, 1, []bool{true}); err != nil {
		t.Fatal(err)
	}
	wantEx(t, e.WriteCoils(allowed, 1, []bool{true}), modbus.ExServerDeviceBusy)
}

func TestWriteCCURejects(t *testing.T) {
	e, _, set := setupWrite(t)
	set.err[22] = rega.ErrRejected
	// multi-write: setpoint ok, mode rejected by the CCU -> 04
	words := append(f32(22), 1)
	wantEx(t, e.WriteRegisters(allowed, 0, words), modbus.ExServerDeviceFail)
	if set.n() != 2 {
		t.Fatalf("calls %d", set.n())
	}
	l := e.WriteLog()
	if l[len(l)-1].Result != "CCU hat abgelehnt" {
		t.Fatalf("log %+v", l[len(l)-1])
	}
}

func TestWriteDisabledAndOrphan(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, writeMapping(t), pol, nil)
	e.SetCatalog(writeCatalog())
	wantEx(t, e.WriteRegisters(allowed, 0, f32(21)), modbus.ExIllegalFunction) // no writer
	allow, _ := ParseAllow([]string{"192.168.3.50"})
	set := &fakeSetter{}
	e.SetWriter(set, WriteConfig{Allow: allow})
	// CCU withdraws the write permission: entry becomes orphan
	cat := writeCatalog()
	cat.Devices[0].Channels[1].Points[0].Writable = false
	e.SetCatalog(cat)
	wantEx(t, e.WriteRegisters(allowed, 0, f32(21)), modbus.ExIllegalDataAddress)
	if set.n() != 0 {
		t.Fatal("orphan written")
	}
}

func TestParseAllow(t *testing.T) {
	n, err := ParseAllow([]string{"192.168.3.50", "10.0.0.0/8", "fd00::/64", ""})
	if err != nil || len(n) != 3 {
		t.Fatalf("%v %v", n, err)
	}
	if got := FormatAllow(n); got[0] != "192.168.3.50" || got[1] != "10.0.0.0/8" {
		t.Fatalf("%v", got)
	}
	for _, bad := range []string{"192.168.3.300", "abc", "10.0.0.0/33"} {
		if _, err := ParseAllow([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestConcurrentWritersShareRateLimit(t *testing.T) {
	e, _, set := setupWrite(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(v float32) {
			defer wg.Done()
			e.WriteRegisters(allowed, 0, f32(v))
		}(float32(21 + i))
	}
	wg.Wait()
	if n := set.n(); n != 1 {
		t.Fatalf("%d writes passed the rate limit concurrently", n)
	}
}

// blockingSetter blocks the first SetValue until released.
type blockingSetter struct {
	fakeSetter
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingSetter) SetValue(ctx context.Context, id int, t rega.Type, v float64) error {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.fakeSetter.SetValue(ctx, id, t, v)
}

// Regression W1: a change of the exports while a multi-write is running
// stops the remaining values.
func TestRevocationDuringMultiWrite(t *testing.T) {
	e, _, _ := setupWrite(t)
	allow, _ := ParseAllow([]string{"192.168.3.0/24"})
	bs := &blockingSetter{fakeSetter: fakeSetter{err: map[int]error{}}, started: make(chan struct{}), release: make(chan struct{})}
	e.SetWriter(bs, WriteConfig{Allow: allow, MinInterval: time.Hour})
	done := make(chan error)
	go func() { done <- e.WriteRegisters(allowed, 0, append(f32(22), 2)) }()
	<-bs.started
	// revoke write permission of the mode while the setpoint is being sent
	m := e.Mapping()
	if _, err := m.Add(mapping.Entry{Key: "dp:HmIP-RF.T1:1.SET_POINT_MODE", Kind: "dp", Type: rega.Enum, Format: mapping.Uint16}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetMapping(m); err != nil {
		t.Fatal(err)
	}
	close(bs.release)
	wantEx(t, <-done, modbus.ExServerDeviceFail)
	if n := bs.n(); n != 1 {
		t.Fatalf("%d values sent, only the first may have gone out", n)
	}
	l := e.WriteLog()
	if last := l[len(l)-1]; last.Key != "dp:HmIP-RF.T1:1.SET_POINT_MODE" || !strings.HasPrefix(last.Result, "nicht ausgeführt") {
		t.Fatalf("log %+v", last)
	}
}

// Regression W2: the allow list is copied.
func TestAllowListIsCopied(t *testing.T) {
	e, _, set := setupWrite(t)
	allow, _ := ParseAllow([]string{"192.168.3.50"})
	e.SetWriter(set, WriteConfig{Allow: allow})
	allow[0].IP = net.ParseIP("10.0.0.9").To16()
	allow[0].Mask = net.CIDRMask(0, 128)
	wantEx(t, e.WriteRegisters(stranger, 0, f32(21)), modbus.ExIllegalFunction)
	if err := e.WriteRegisters(allowed, 0, f32(21)); err != nil {
		t.Fatal(err)
	}
}

// Regression W2: changing the catalog after SetCatalog has no effect.
func TestCatalogIsCopied(t *testing.T) {
	e, _, set := setupWrite(t)
	cat := writeCatalog()
	e.SetCatalog(cat)
	*cat.Devices[0].Channels[1].Points[0].Max = 1000
	wantEx(t, e.WriteRegisters(allowed, 0, f32(500)), modbus.ExIllegalDataValue)
	if set.n() != 0 {
		t.Fatal("value sent")
	}
}

// Regression W4: slots of values that were not executed are given back.
func TestRejectedMultiWriteReleasesSlots(t *testing.T) {
	e, _, set := setupWrite(t)
	set.err[21] = rega.ErrRejected
	wantEx(t, e.WriteRegisters(allowed, 0, append(f32(22), 1)), modbus.ExServerDeviceFail)
	if set.n() != 1 {
		t.Fatalf("calls %d", set.n())
	}
	// the mode was never sent: it must be writable immediately
	if err := e.WriteRegisters(allowed, 2, []uint16{1}); err != nil {
		t.Fatalf("slot of the unsent value still reserved: %v", err)
	}
	// the rejected setpoint itself keeps its slot (it was sent)
	wantEx(t, e.WriteRegisters(allowed, 0, f32(23)), modbus.ExServerDeviceBusy)
	found := false
	for _, l := range e.WriteLog() {
		if l.Result == "nicht ausgeführt: vorheriger Wert abgelehnt" {
			found = true
		}
	}
	if !found {
		t.Fatal("unsent value not logged")
	}
}

// Regression W3: no write without a known range.
func TestNoWriteWithoutRange(t *testing.T) {
	e, _, set := setupWrite(t)
	cat := writeCatalog()
	cat.Devices[0].Channels[1].Points[0].Max = nil
	e.SetCatalog(cat)
	// CheckFormat already orphans float32 without range? float32 for FLOAT
	// needs no range, so the export stays; the write must be refused.
	wantEx(t, e.WriteRegisters(allowed, 0, f32(21)), modbus.ExIllegalDataValue)
	if set.n() != 0 {
		t.Fatal("value sent")
	}
}

// Regression (E2E test CCU 2026-11-11): writing the old value back right
// after a change must not be acknowledged as "unchanged" while the cache
// still holds the value from before the change.
func TestToggleBackIsNotUnchanged(t *testing.T) {
	e, pol, set := setupWrite(t)
	e.SetWriter(set, WriteConfig{Allow: mustAllow(t), MinInterval: 50 * time.Millisecond})
	// CCU: switch false
	if err := e.WriteCoils(allowed, 0, []bool{true}); err != nil {
		t.Fatal(err)
	}
	// cache still says false (no poll yet): writing false back is a real
	// change -> rate limited, never "unchanged"
	wantEx(t, e.WriteCoils(allowed, 0, []bool{false}), modbus.ExServerDeviceBusy)
	time.Sleep(60 * time.Millisecond)
	if err := e.WriteCoils(allowed, 0, []bool{false}); err != nil {
		t.Fatal(err)
	}
	if set.n() != 2 {
		t.Fatalf("toggle back not forwarded: %d calls", set.n())
	}
	// after a poll newer than the write, an equal value is unchanged again
	pol.set(map[int]rega.Reading{20: {Found: true, Value: "false", Timestamp: 1}, 23: {Found: true, Value: "false", Timestamp: 2}}, time.Now())
	if err := e.WriteCoils(allowed, 0, []bool{false}); err != nil {
		t.Fatal(err)
	}
	if set.n() != 2 {
		t.Fatal("unchanged value forwarded after fresh poll")
	}
}

func mustAllow(t *testing.T) []*net.IPNet {
	t.Helper()
	a, err := ParseAllow([]string{"192.168.3.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Regression N1: a newer cycle in general is not enough; the reading of
// the written object itself must come from a cycle after the write.
func TestSameNeedsPerIDReadAfterWrite(t *testing.T) {
	e, pol, set := setupWrite(t)
	e.SetWriter(set, WriteConfig{Allow: mustAllow(t), MinInterval: time.Nanosecond})
	if err := e.WriteCoils(allowed, 0, []bool{true}); err != nil {
		t.Fatal(err)
	}
	// global cycle is newer, but ID 23 still carries its old reading
	old := pol.ReadAt(23)
	pol.set(map[int]rega.Reading{20: {Found: true, Value: "false", Timestamp: 1}, 23: {Found: true, Value: "false", Timestamp: 1}}, time.Now().Add(time.Second))
	pol.mu.Lock()
	pol.readAt[23] = old
	pol.mu.Unlock()
	if err := e.WriteCoils(allowed, 0, []bool{false}); err != nil {
		t.Fatal(err)
	}
	if set.n() != 2 {
		t.Fatalf("toggle back acknowledged from stale per-ID reading: %d calls", set.n())
	}
}
