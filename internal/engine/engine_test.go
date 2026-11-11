// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/engine/engine_test.go
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
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/mapping"
	"github.com/WolfHenk/CCU-ModbusServer/internal/poller"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

type fakePoller struct {
	mu     sync.Mutex
	items  []int
	values map[int]rega.Reading
	health poller.Health
	readAt map[int]time.Time
}

func (p *fakePoller) ReadAt(id int) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readAt[id]
}

func (p *fakePoller) SetItems(ids []int) {
	p.mu.Lock()
	p.items = append([]int(nil), ids...)
	p.mu.Unlock()
}

func (p *fakePoller) Values() (map[int]rega.Reading, poller.Health) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[int]rega.Reading{}
	for k, v := range p.values {
		out[k] = v
	}
	return out, p.health
}

func (p *fakePoller) set(v map[int]rega.Reading, lastOK time.Time) {
	p.mu.Lock()
	p.values = v
	p.health = poller.Health{OK: true, LastOK: lastOK}
	p.readAt = map[int]time.Time{}
	for id := range v {
		p.readAt[id] = lastOK
	}
	p.mu.Unlock()
}

type sink struct{ t *table.Table }

func (s *sink) SetTable(t *table.Table) { s.t = t }

func fp(v float64) *float64 { return &v }

var status = []table.Slot{{Kind: table.Single}, {Kind: table.First}, {Kind: table.Second}, {Kind: table.Single}}

// Valve with UNREACH (id 10), temperature (11), valve state (12), boost
// action (13); system variable 950 (bool, writable).
func testCatalog() *rega.Catalog {
	return &rega.Catalog{
		Devices: []rega.Device{{Interface: "BidCos-RF", Address: "V1", Channels: []rega.Channel{
			{Address: "V1:0", Points: []rega.Point{{ReGaID: 10, ID: "BidCos-RF.V1:0.UNREACH", Param: "UNREACH", Type: rega.Bool, Readable: true}}},
			{Address: "V1:4", Points: []rega.Point{
				{ReGaID: 11, ID: "BidCos-RF.V1:4.ACTUAL_TEMPERATURE", Param: "ACTUAL_TEMPERATURE", Type: rega.Float, Min: fp(-10), Max: fp(56), Readable: true},
				{ReGaID: 12, ID: "BidCos-RF.V1:4.VALVE_STATE", Param: "VALVE_STATE", Type: rega.Integer, Min: fp(0), Max: fp(99), Readable: true},
				{ReGaID: 13, ID: "BidCos-RF.V1:4.BOOST_MODE", Param: "BOOST_MODE", Type: rega.Action, Writable: true},
			}},
		}}},
		SysVars: []rega.SysVar{{ReGaID: 950, ID: "SV:950", Name: "Anwesenheit", Type: rega.Bool, Writable: true}},
	}
}

func testMapping(t *testing.T) *mapping.File {
	t.Helper()
	m := mapping.New(4)
	for _, e := range []mapping.Entry{
		{Key: "dp:BidCos-RF.V1:4.ACTUAL_TEMPERATURE", Kind: "dp", Type: rega.Float, Format: mapping.Float32},        // IR 4-5, export 1
		{Key: "dp:BidCos-RF.V1:4.VALVE_STATE", Kind: "dp", Type: rega.Integer, Format: mapping.Uint16},              // IR 6, export 2
		{Key: "dp:BidCos-RF.V1:4.BOOST_MODE", Kind: "dp", Type: rega.Action, Format: mapping.Bit, Writable: true},   // coil 0, export 3
		{Key: "sv:950", Kind: "sv", SysVarID: 950, SysVarName: "Anwesenheit", Type: rega.Bool, Format: mapping.Bit}, // DI 0, export 4
	} {
		if _, err := m.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func newEngine(t *testing.T, m *mapping.File, pol Poller, sk TableSink) *Engine {
	t.Helper()
	e, err := New(m, pol, sk, status, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func read(t *testing.T, tab *table.Table, a table.Area, addr, n int) []uint16 {
	t.Helper()
	v, err := tab.Read(a, addr, n)
	if err != nil {
		t.Fatalf("%s %d: %v", a, addr, err)
	}
	return v
}

func bits(t *testing.T, tab *table.Table, export int) (valid, warn uint16) {
	return read(t, tab, table.DiscreteInputs, mapping.ValidBase+export, 1)[0], read(t, tab, table.DiscreteInputs, mapping.WarnBase+export, 1)[0]
}

func TestValuesAndBits(t *testing.T) {
	pol := &fakePoller{}
	sk := &sink{}
	e := newEngine(t, testMapping(t), pol, sk)
	e.SetCatalog(testCatalog())
	ids := append([]int(nil), pol.items...)
	sort.Ints(ids)
	if len(ids) != 5 || ids[0] != 10 || ids[4] != 950 {
		t.Fatalf("poll items %v (UNREACH must be included)", ids)
	}
	if sk.t == nil || sk.t != e.Table() {
		t.Fatal("table not handed to the server")
	}
	now := time.Now()
	pol.set(map[int]rega.Reading{
		10:  {Found: true, Value: "false", Timestamp: 1},
		11:  {Found: true, Value: "21.500000", Timestamp: 100},
		12:  {Found: true, Value: "42", Timestamp: 100},
		13:  {Found: true, Value: "", Timestamp: 0},
		950: {Found: true, Value: "true", Timestamp: 5},
	}, now)
	e.OnCycle(nil, poller.Health{})
	tab := e.Table()
	if v := read(t, tab, table.InputRegisters, 4, 2); v[0] != 0x41AC || v[1] != 0 {
		t.Fatalf("temperature %04x", v)
	}
	if v := read(t, tab, table.InputRegisters, 6, 1); v[0] != 42 {
		t.Fatalf("valve %v", v)
	}
	if v := read(t, tab, table.DiscreteInputs, 0, 1); v[0] != 1 {
		t.Fatalf("sysvar %v", v)
	}
	if v := read(t, tab, table.Coils, 0, 1); v[0] != 0 {
		t.Fatal("action must read 0")
	}
	for _, x := range []int{1, 2, 4} {
		if vb, wb := bits(t, tab, x); vb != 1 || wb != 0 {
			t.Errorf("export %d bits %d/%d", x, vb, wb)
		}
	}
	if vb, _ := bits(t, tab, 3); vb != 0 {
		t.Error("never-set action marked valid")
	}

	// UNREACH = true: values stay valid, warning bit set (Wolfram)
	pol.set(map[int]rega.Reading{
		10:  {Found: true, Value: "true", Timestamp: 2},
		11:  {Found: true, Value: "22.000000", Timestamp: 200},
		12:  {Found: true, Value: "42", Timestamp: 200},
		950: {Found: true, Value: "true", Timestamp: 5},
	}, now)
	e.Refresh(now)
	if vb, wb := bits(t, tab, 1); vb != 1 || wb != 1 {
		t.Fatalf("unreach: valid %d warn %d", vb, wb)
	}
	if _, wb := bits(t, tab, 4); wb != 0 {
		t.Fatal("system variable got a device warning")
	}

	// CCU logic silent too long: invalid, float NaN, integer keeps last
	e.Refresh(now.Add(time.Minute))
	if vb, _ := bits(t, tab, 1); vb != 0 {
		t.Fatal("still valid after source down")
	}
	if v := read(t, tab, table.InputRegisters, 4, 2); v[0] != 0x7FC0 {
		t.Fatalf("float substitute %04x", v)
	}
	if v := read(t, tab, table.InputRegisters, 6, 1); v[0] != 42 {
		t.Fatalf("integer substitute %v", v)
	}
}

func TestOrphanAndTypeChange(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, testMapping(t), pol, nil)
	cat := testCatalog()
	// temperature datapoint vanished, valve state became a string
	cat.Devices[0].Channels[1].Points = []rega.Point{
		{ReGaID: 12, ID: "BidCos-RF.V1:4.VALVE_STATE", Param: "VALVE_STATE", Type: rega.String, Readable: true},
		{ReGaID: 13, ID: "BidCos-RF.V1:4.BOOST_MODE", Param: "BOOST_MODE", Type: rega.Action, Writable: false},
	}
	e.SetCatalog(cat)
	pol.set(map[int]rega.Reading{10: {Found: true, Value: "false", Timestamp: 1}, 950: {Found: true, Value: "false", Timestamp: 1}}, time.Now())
	e.Refresh(time.Now())
	tab := e.Table()
	for _, x := range []int{1, 2, 3} {
		if vb, _ := bits(t, tab, x); vb != 0 {
			t.Errorf("orphan export %d valid", x)
		}
	}
	// orphaned writable entry is not writable any more, address kept
	if err := tab.CheckWrite(table.Coils, 0, 1); err == nil {
		t.Error("orphan still writable")
	}
	if vb, _ := bits(t, tab, 4); vb != 1 {
		t.Error("sysvar lost")
	}
	if m := e.Mapping(); m.Entries[0].Orphan {
		t.Error("orphan flag leaked into the stored mapping")
	}
}

func TestSysVarFoundByNameAfterRestore(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, testMapping(t), pol, nil)
	cat := testCatalog()
	cat.SysVars[0].ReGaID = 4711 // restore gave a new ID
	e.SetCatalog(cat)
	found := false
	for _, id := range pol.items {
		if id == 4711 {
			found = true
		}
	}
	if !found {
		t.Fatalf("system variable not found by name: %v", pol.items)
	}
}

func TestRemoveDropsBitsAndStatus(t *testing.T) {
	pol := &fakePoller{}
	sk := &sink{}
	m := testMapping(t)
	e := newEngine(t, m, pol, sk)
	e.SetCatalog(testCatalog())
	e.SetStatus(1, 70000)
	m2 := e.Mapping()
	m2.Remove("dp:BidCos-RF.V1:4.VALVE_STATE")
	if err := e.SetMapping(m2); err != nil {
		t.Fatal(err)
	}
	tab := sk.t
	if _, err := tab.Read(table.DiscreteInputs, mapping.ValidBase+2, 1); err != nil {
		t.Fatal(err) // reserved gap inside the block reads 0
	}
	if v := read(t, tab, table.InputRegisters, 0, 4); v[0] != 1 || v[1] != 1 || v[2] != 4464 {
		t.Fatalf("status not carried over: %v", v)
	}
	e.SetStatus(1, 5)
	if v := read(t, tab, table.InputRegisters, 3, 1); v[0] != 3 {
		t.Fatalf("export count %v", v)
	}
	if err := tab.CheckWrite(table.InputRegisters, 6, 1); err == nil {
		t.Fatal("gap writable")
	}
}

// blockingSink records whether the table it receives is already filled
// and can block to provoke races.
type blockingSink struct {
	mu      sync.Mutex
	tables  []*table.Table
	release chan struct{}
	entered chan struct{}
	checks  []uint16
}

func (s *blockingSink) SetTable(t *table.Table) {
	v, _ := t.Read(table.DiscreteInputs, mapping.ValidBase+1, 1)
	s.mu.Lock()
	s.tables = append(s.tables, t)
	s.checks = append(s.checks, v[0])
	rel, ent := s.release, s.entered
	s.release, s.entered = nil, nil
	s.mu.Unlock()
	if rel != nil {
		close(ent)
		<-rel
	}
}

// Regression E1: a rebuilt table is complete before it is handed over.
func TestNewTableIsFilledBeforeHandOver(t *testing.T) {
	pol := &fakePoller{}
	sk := &blockingSink{}
	e := newEngine(t, testMapping(t), pol, sk)
	pol.set(map[int]rega.Reading{10: {Found: true, Value: "false", Timestamp: 1}, 11: {Found: true, Value: "21.500000", Timestamp: 9}}, time.Now())
	e.SetCatalog(testCatalog())
	sk.mu.Lock()
	last := sk.checks[len(sk.checks)-1]
	sk.mu.Unlock()
	if last != 1 {
		t.Fatal("table handed over before its values and valid bits were filled")
	}
}

// Regression E1/E4: concurrent rebuilds are serialized; the later one
// wins for both the poller selection and the table.
func TestConcurrentRebuildsAreSerialized(t *testing.T) {
	pol := &fakePoller{}
	sk := &blockingSink{}
	e := newEngine(t, testMapping(t), pol, sk)
	e.SetCatalog(testCatalog())

	sk.mu.Lock()
	sk.release = make(chan struct{})
	sk.entered = make(chan struct{})
	rel, ent := sk.release, sk.entered
	sk.mu.Unlock()

	// first rebuild (catalog without the system variable) blocks in SetTable
	cat1 := testCatalog()
	cat1.SysVars = nil
	done1 := make(chan struct{})
	go func() { e.SetCatalog(cat1); close(done1) }()
	<-ent
	// second rebuild must wait for the first, then win
	done2 := make(chan struct{})
	go func() { e.SetCatalog(testCatalog()); close(done2) }()
	select {
	case <-done2:
		t.Fatal("second rebuild overtook the first")
	case <-time.After(50 * time.Millisecond):
	}
	close(rel)
	<-done1
	<-done2
	has950 := false
	pol.mu.Lock()
	for _, id := range pol.items {
		if id == 950 {
			has950 = true
		}
	}
	pol.mu.Unlock()
	sk.mu.Lock()
	lastTab := sk.tables[len(sk.tables)-1]
	sk.mu.Unlock()
	if !has950 || lastTab != e.Table() {
		t.Fatalf("older rebuild won: items %v", pol.items)
	}
}

// Regression E2: invalid mappings are rejected, the engine keeps its
// state, and returned mappings are copies.
func TestSetMappingValidatesAndCopies(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, testMapping(t), pol, nil)
	before := e.Table()
	bad := e.Mapping()
	bad.Next[table.HoldingRegisters] = -5
	if err := e.SetMapping(bad); err == nil {
		t.Fatal("negative next accepted")
	}
	bad = e.Mapping()
	bad.Next[table.InputRegisters] = 1 << 30
	if err := e.SetMapping(bad); err == nil {
		t.Fatal("huge next accepted")
	}
	if e.Table() != before {
		t.Fatal("table replaced by an invalid mapping")
	}
	m := e.Mapping()
	m.Entries[0].Label = "changed outside"
	if e.Mapping().Entries[0].Label == "changed outside" {
		t.Fatal("Mapping() exposes internal state")
	}
	in := testMapping(t)
	e2 := newEngine(t, in, pol, nil)
	in.Entries[0].Label = "changed after New"
	if e2.Mapping().Entries[0].Label == "changed after New" {
		t.Fatal("New keeps the caller's pointer")
	}
	if _, err := New(bad, pol, nil, status, time.Second); err == nil {
		t.Fatal("New accepted an invalid mapping")
	}
}

// Regression E3: after a format change the old words are not used as
// substitute.
func TestFormatChangeDropsSubstitute(t *testing.T) {
	pol := &fakePoller{}
	sk := &sink{}
	e := newEngine(t, testMapping(t), pol, sk)
	e.SetCatalog(testCatalog())
	now := time.Now()
	pol.set(map[int]rega.Reading{10: {Found: true, Value: "false", Timestamp: 1}, 12: {Found: true, Value: "42", Timestamp: 5}}, now)
	e.Refresh(now)
	if v := read(t, sk.t, table.InputRegisters, 6, 1); v[0] != 42 {
		t.Fatalf("valve %v", v)
	}
	// the value goes missing, then the valve is re-mapped as int16 x10
	// (different representation, same address)
	pol.set(map[int]rega.Reading{10: {Found: true, Value: "false", Timestamp: 1}}, now)
	e.Refresh(now)
	if v := read(t, sk.t, table.InputRegisters, 6, 1); v[0] != 42 {
		t.Fatalf("substitute in the old format expected: %v", v)
	}
	m := e.Mapping()
	if _, err := m.Add(mapping.Entry{Key: "dp:BidCos-RF.V1:4.VALVE_STATE", Kind: "dp", Type: rega.Integer, Format: mapping.Int16, Scale: 10}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetMapping(m); err != nil {
		t.Fatal(err)
	}
	// still missing: the old 42 (uint16) must not be used for int16 x10
	e.Refresh(now)
	if v := read(t, sk.t, table.InputRegisters, 6, 1); v[0] != 0 {
		t.Fatalf("old substitute reused after format change: %v", v)
	}
}

// Regression R4: when the CCU gives an ID to a different object, the old
// cached reading must not be published as valid for the new entry.
func TestReusedIDIsUnknownUntilNextCycle(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, writeMapping(t), pol, nil)
	e.SetCatalog(writeCatalog())
	vals := map[int]rega.Reading{
		20: {Found: true, Value: "false", Timestamp: 1},
		21: {Found: true, Value: "20.000000", Timestamp: 1},
		25: {Found: true, Value: "19.500000", Timestamp: 1},
	}
	pol.set(vals, time.Now())
	e.OnCycle(vals, poller.Health{})
	// export 1 = setpoint (id 21), export 5 = actual temperature (id 25)
	if v, _ := bits(t, e.Table(), 5); v != 1 {
		t.Fatal("actual temperature should be valid")
	}
	cat := writeCatalog()
	pts := cat.Devices[0].Channels[1].Points
	pts[0].ReGaID, pts[4].ReGaID = 25, 21 // IDs swapped by the CCU
	e.SetCatalog(cat)
	for _, n := range []int{1, 5} {
		if v, _ := bits(t, e.Table(), n); v != 0 {
			t.Fatalf("export %d valid with a reading of another object", n)
		}
	}
	// K2: a cycle that started before the reassignment (ReadAt old) does
	// not clear the flag
	e.OnCycle(vals, poller.Health{})
	for _, n := range []int{1, 5} {
		if v, _ := bits(t, e.Table(), n); v != 0 {
			t.Fatalf("export %d valid after a cycle that started before the rebuild", n)
		}
	}
	// only ID 25 read again after the rebuild: only its entry recovers
	time.Sleep(2 * time.Millisecond)
	pol.mu.Lock()
	pol.readAt[25] = time.Now()
	pol.mu.Unlock()
	e.OnCycle(vals, poller.Health{})
	// setpoint entry (export 1) now uses ID 25, actual temp (5) ID 21
	if v, _ := bits(t, e.Table(), 1); v != 1 {
		t.Fatal("entry with re-read ID not valid again")
	}
	if v, _ := bits(t, e.Table(), 5); v != 0 {
		t.Fatal("entry whose ID was not re-read became valid")
	}
}

// K1: the engine owns its catalog; changes of the caller's copy after
// SetCatalog must not reach it (run with -race).
func TestCatalogIsCopiedOnEntry(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, writeMapping(t), pol, nil)
	cat := writeCatalog()
	e.SetCatalog(cat)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p := &cat.Devices[0].Channels[1].Points[0]
			p.Writable = i%2 == 0
			*p.Max = float64(i)
			p.ReGaID = 9000 + i
		}
	}()
	for i := 0; i < 50; i++ {
		if err := e.SetMapping(e.Mapping()); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	if err := e.SetMapping(e.Mapping()); err != nil {
		t.Fatal(err)
	}
	for _, o := range e.objs {
		if o.entry.Key == "dp:HmIP-RF.T1:1.SET_POINT_TEMPERATURE" {
			if o.regaID != 21 || !o.meta.writable || o.meta.max == nil || *o.meta.max != 30.5 {
				t.Fatalf("engine follows the caller's catalog: id %d writable %v max %v", o.regaID, o.meta.writable, o.meta.max)
			}
			return
		}
	}
	t.Fatal("entry missing")
}

// Regression R3: a rejected mapping leaves the active state untouched.
func TestRejectedMappingKeepsState(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, writeMapping(t), pol, nil)
	e.SetCatalog(writeCatalog())
	before, tab := e.Mapping(), e.Table()
	bad := before.Clone()
	bad.Entries[0].Address = bad.Entries[1].Address // overlap
	if err := e.SetMapping(bad); err == nil {
		t.Fatal("overlapping mapping accepted")
	}
	if e.Table() != tab || len(e.Mapping().Entries) != len(before.Entries) || e.Mapping().Entries[0].Address != before.Entries[0].Address {
		t.Fatal("state changed by a rejected mapping")
	}
}

// U3: deselected entries stay on the client sheet with their address.
func TestSheetListsReservedAddresses(t *testing.T) {
	pol := &fakePoller{}
	e := newEngine(t, writeMapping(t), pol, nil)
	e.SetCatalog(writeCatalog())
	m := e.Mapping()
	if !m.Remove("dp:HmIP-RF.T1:1.SET_POINT_MODE") {
		t.Fatal("remove")
	}
	if err := e.SetMapping(m); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range e.Sheet(time.Now()) {
		if r.Key == "dp:HmIP-RF.T1:1.SET_POINT_MODE" {
			found = true
			if !r.Reserved || r.Address != 2 || r.Table != table.HoldingRegisters.String() || r.Valid || r.FCWrite != "" || r.ValidBit != mapping.ValidBase+2 {
				t.Fatalf("reserved row %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("deselected entry missing from the sheet")
	}
}
