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
	e := New(testMapping(t), pol, sk, status, 10*time.Second)
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
	e := New(testMapping(t), pol, nil, status, 10*time.Second)
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
	e := New(testMapping(t), pol, nil, status, 10*time.Second)
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
	e := New(m, pol, sk, status, 10*time.Second)
	e.SetCatalog(testCatalog())
	e.SetStatus(1, 70000)
	m2 := *m
	m2.Entries = append([]mapping.Entry(nil), m.Entries...)
	m2.Remove("dp:BidCos-RF.V1:4.VALVE_STATE")
	e.SetMapping(&m2)
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
