// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/table/table_test.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.1.0
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

package table

import (
	"errors"
	"testing"
)

func TestValidateRejectsBrokenLayouts(t *testing.T) {
	cases := map[string]Layout{}
	var l Layout
	l.Slots[InputRegisters] = []Slot{{Kind: First}}
	cases["first without second"] = l
	l = Layout{}
	l.Slots[InputRegisters] = []Slot{{Kind: Second}}
	cases["second alone"] = l
	l = Layout{}
	l.Slots[InputRegisters] = []Slot{{Kind: Single, Writable: true}}
	cases["writable input register"] = l
	l = Layout{}
	l.Slots[DiscreteInputs] = []Slot{{Kind: Single, Writable: true}}
	cases["writable discrete input"] = l
	l = Layout{}
	l.Slots[Coils] = []Slot{{Kind: First}, {Kind: Second}}
	cases["pair in coils"] = l
	l = Layout{}
	l.Slots[HoldingRegisters] = []Slot{{Kind: First, Writable: true}, {Kind: Second}}
	cases["halves differ"] = l
	l = Layout{}
	l.Slots[HoldingRegisters] = []Slot{{Kind: Reserved, Writable: true}}
	cases["writable gap"] = l
	l = Layout{}
	l.Slots[HoldingRegisters] = make([]Slot, MaxSlots+1)
	cases["too large"] = l
	for name, l := range cases {
		if _, err := New(l); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func testTable(t *testing.T) *Table {
	t.Helper()
	var l Layout
	// holding: 0 single, 1-2 pair, 3 gap, 4 read-only single
	l.Slots[HoldingRegisters] = []Slot{
		{Kind: Single, Writable: true},
		{Kind: First, Writable: true}, {Kind: Second, Writable: true},
		{Kind: Reserved},
		{Kind: Single},
	}
	tab, err := New(l)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestReadSplitAndRange(t *testing.T) {
	tab := testTable(t)
	ok := [][2]int{{0, 5}, {0, 1}, {1, 2}, {0, 3}, {3, 2}, {4, 1}}
	for _, r := range ok {
		if _, err := tab.Read(HoldingRegisters, r[0], r[1]); err != nil {
			t.Errorf("read %v: %v", r, err)
		}
	}
	bad := [][2]int{{0, 2}, {2, 1}, {2, 3}, {1, 1}, {0, 6}, {5, 1}, {0, 0}, {-1, 1}}
	for _, r := range bad {
		if _, err := tab.Read(HoldingRegisters, r[0], r[1]); !errors.Is(err, ErrIllegalAddress) {
			t.Errorf("read %v: err=%v, want illegal address", r, err)
		}
	}
}

func TestCheckWrite(t *testing.T) {
	tab := testTable(t)
	if err := tab.CheckWrite(HoldingRegisters, 0, 3); err != nil {
		t.Errorf("0..2: %v", err)
	}
	for _, r := range [][2]int{{1, 1}, {2, 1}, {0, 4}, {3, 1}, {4, 1}, {0, 2}} {
		if err := tab.CheckWrite(HoldingRegisters, r[0], r[1]); !errors.Is(err, ErrIllegalAddress) {
			t.Errorf("write %v: err=%v", r, err)
		}
	}
	if err := tab.CheckWrite(InputRegisters, 0, 1); err == nil {
		t.Error("input register write accepted")
	}
}

func TestGapStaysZeroAndUpdateIsAtomic(t *testing.T) {
	tab := testTable(t)
	tab.Update(func(w Writer) { w.Set(HoldingRegisters, 0, 7, 8, 9, 10, 11) })
	v, err := tab.Read(HoldingRegisters, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{7, 8, 9, 0, 11}
	for i := range want {
		if v[i] != want[i] {
			t.Fatalf("got %v want %v", v, want)
		}
	}
}

func TestCount(t *testing.T) {
	tab := testTable(t)
	if n := tab.Count(HoldingRegisters); n != 3 {
		t.Fatalf("count %d, want 3", n)
	}
}
