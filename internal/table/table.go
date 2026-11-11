// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/table/table.go
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

// Package table holds the four Modbus data tables (coils, discrete inputs,
// holding registers, input registers) as one consistent image.
//
// Every address of a table is a slot. A slot is either a reserved gap
// (reads 0, never writable), a single bit/register, or one half of a
// 32-bit value that occupies two consecutive registers. All reads are
// taken under one lock, so a client never sees a half-updated 32-bit value.
package table

import (
	"errors"
	"fmt"
	"sync"
)

// Area identifies one of the four Modbus tables.
type Area uint8

const (
	Coils Area = iota
	DiscreteInputs
	HoldingRegisters
	InputRegisters
	numAreas
)

// String returns the usual Modbus name of the area.
func (a Area) String() string {
	switch a {
	case Coils:
		return "Coil"
	case DiscreteInputs:
		return "Discrete Input"
	case HoldingRegisters:
		return "Holding Register"
	case InputRegisters:
		return "Input Register"
	}
	return fmt.Sprintf("Area(%d)", uint8(a))
}

// IsBit reports whether the area holds single bits.
func (a Area) IsBit() bool { return a == Coils || a == DiscreteInputs }

// AllowsWrite reports whether Modbus allows writes to the area at all.
func (a Area) AllowsWrite() bool { return a == Coils || a == HoldingRegisters }

// Kind describes what a slot holds.
type Kind uint8

const (
	// Reserved is a gap: an address that was assigned once and is kept so
	// that later addresses never move. It reads 0 and cannot be written.
	Reserved Kind = iota
	// Single is one bit or one 16-bit register.
	Single
	// First is the first register of a 32-bit value.
	First
	// Second is the second register of a 32-bit value.
	Second
)

// Slot describes one address.
type Slot struct {
	Kind     Kind
	Writable bool
}

// MaxSlots is the size of a Modbus address space.
const MaxSlots = 65536

// Layout lists the slots of all four areas, index = Modbus address.
type Layout struct {
	Slots [numAreas][]Slot
}

// ErrIllegalAddress is returned for addresses that are outside a table,
// split a 32-bit value, or are not writable. It maps to Modbus exception 02.
var ErrIllegalAddress = errors.New("illegal data address")

// Validate checks that the layout is internally consistent.
func (l *Layout) Validate() error {
	for a := Area(0); a < numAreas; a++ {
		s := l.Slots[a]
		if len(s) > MaxSlots {
			return fmt.Errorf("%s: %d slots exceed %d", a, len(s), MaxSlots)
		}
		for i := 0; i < len(s); i++ {
			sl := s[i]
			if sl.Writable && !a.AllowsWrite() {
				return fmt.Errorf("%s %d: writable slot in read-only area", a, i)
			}
			if sl.Writable && sl.Kind == Reserved {
				return fmt.Errorf("%s %d: reserved slot marked writable", a, i)
			}
			switch sl.Kind {
			case Reserved, Single:
			case First:
				if a.IsBit() {
					return fmt.Errorf("%s %d: 32-bit value in bit area", a, i)
				}
				if i+1 >= len(s) || s[i+1].Kind != Second {
					return fmt.Errorf("%s %d: first half without second half", a, i)
				}
				if s[i+1].Writable != sl.Writable {
					return fmt.Errorf("%s %d: halves differ in writability", a, i)
				}
				i++ // second half checked
			case Second:
				return fmt.Errorf("%s %d: second half without first half", a, i)
			default:
				return fmt.Errorf("%s %d: unknown slot kind %d", a, i, sl.Kind)
			}
		}
	}
	return nil
}

// Table is the live, lock-protected data image.
type Table struct {
	mu     sync.RWMutex
	layout Layout
	vals   [numAreas][]uint16 // bit areas store 0 or 1
}

// New creates a table for the layout. All values start at 0.
func New(l Layout) (*Table, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	t := &Table{}
	for a := Area(0); a < numAreas; a++ {
		t.layout.Slots[a] = append([]Slot(nil), l.Slots[a]...)
		t.vals[a] = make([]uint16, len(l.Slots[a]))
	}
	return t, nil
}

// Len returns the number of addresses of an area.
func (t *Table) Len(a Area) int {
	return len(t.layout.Slots[a])
}

// Count returns the number of assigned (non-reserved) slots of an area;
// a 32-bit value counts once.
func (t *Table) Count(a Area) int {
	n := 0
	for _, s := range t.layout.Slots[a] {
		if s.Kind == Single || s.Kind == First {
			n++
		}
	}
	return n
}

func (t *Table) checkRange(a Area, start, qty int) error {
	if a >= numAreas || qty < 1 || start < 0 || start+qty > len(t.layout.Slots[a]) {
		return ErrIllegalAddress
	}
	s := t.layout.Slots[a]
	if s[start].Kind == Second || s[start+qty-1].Kind == First {
		return ErrIllegalAddress
	}
	return nil
}

// Read copies qty values starting at start into a new slice. Reads that
// begin or end inside a 32-bit value fail with ErrIllegalAddress.
func (t *Table) Read(a Area, start, qty int) ([]uint16, error) {
	if err := t.checkRange(a, start, qty); err != nil {
		return nil, err
	}
	out := make([]uint16, qty)
	t.mu.RLock()
	copy(out, t.vals[a][start:start+qty])
	t.mu.RUnlock()
	return out, nil
}

// CheckWrite reports whether a Modbus client may write qty addresses
// starting at start: the range must lie inside the table, must not split
// a 32-bit value and every slot must be assigned and writable.
func (t *Table) CheckWrite(a Area, start, qty int) error {
	if !a.AllowsWrite() {
		return ErrIllegalAddress
	}
	if err := t.checkRange(a, start, qty); err != nil {
		return err
	}
	for _, s := range t.layout.Slots[a][start : start+qty] {
		if s.Kind == Reserved || !s.Writable {
			return ErrIllegalAddress
		}
	}
	return nil
}

// Slot returns the slot description of an address.
func (t *Table) Slot(a Area, addr int) (Slot, bool) {
	if a >= numAreas || addr < 0 || addr >= len(t.layout.Slots[a]) {
		return Slot{}, false
	}
	return t.layout.Slots[a][addr], true
}

// Writer changes values inside one Update call.
type Writer struct {
	t *Table
}

// Set stores values starting at addr. Reserved slots are left at 0.
// Bit areas store any non-zero value as 1.
func (w Writer) Set(a Area, addr int, values ...uint16) {
	s := w.t.layout.Slots[a]
	v := w.t.vals[a]
	for i, x := range values {
		p := addr + i
		if p < 0 || p >= len(s) || s[p].Kind == Reserved {
			continue
		}
		if a.IsBit() && x != 0 {
			x = 1
		}
		v[p] = x
	}
}

// Update runs fn with exclusive access. All changes made in fn become
// visible to readers at once.
func (t *Table) Update(fn func(w Writer)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(Writer{t: t})
}
