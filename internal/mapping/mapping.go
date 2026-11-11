// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/mapping/mapping.go
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

// Package mapping keeps the assignment of CCU objects to Modbus addresses.
//
// Rules (plan "ERGEBNIS", 2026-11-11):
//   - An address, once assigned, never changes. Deselecting an object
//     leaves a reserved gap; new objects are appended at the end.
//   - Selecting a previously deselected object again gives it back its old
//     address if the area and size still fit.
//   - Identity is the CCU object, never its display name: for datapoints
//     interface + device address + channel + parameter, for system
//     variables the ReGa ID (the name is kept to find it again after a
//     restore).
//   - Objects that disappear from the CCU stay as "orphan" with their
//     address reserved until the user removes them.
//   - Every entry has a stable export number n. Discrete input
//     ValidBase+n is its "valid" bit, WarnBase+n its communication
//     warning bit (UNREACH).
package mapping

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// Fixed discrete input blocks for the per-export status bits.
const (
	ValidBase  = 10000
	WarnBase   = 20000
	MaxExports = 9999
)

// Format is the Modbus representation of a value.
type Format string

const (
	Bit     Format = "bit"     // coil / discrete input
	Int16   Format = "int16"   // one register, signed
	Uint16  Format = "uint16"  // one register
	Int32   Format = "int32"   // two registers
	Uint32  Format = "uint32"  // two registers
	Float32 Format = "float32" // two registers, IEEE 754
)

// Words returns the number of registers (0 for bits).
func (f Format) Words() int {
	switch f {
	case Int16, Uint16:
		return 1
	case Int32, Uint32, Float32:
		return 2
	}
	return 0
}

// Valid reports whether f is a known format.
func (f Format) Valid() bool {
	switch f {
	case Bit, Int16, Uint16, Int32, Uint32, Float32:
		return true
	}
	return false
}

// Entry is one exported object.
type Entry struct {
	Export int    `json:"export"` // stable export number 1..MaxExports
	Key    string `json:"key"`    // identity, see KeyOf
	Kind   string `json:"kind"`   // "dp" or "sv"

	// Datapoint identity (Kind "dp").
	Interface string `json:"interface,omitempty"`
	Device    string `json:"device,omitempty"`  // device address
	Channel   string `json:"channel,omitempty"` // channel address "ADDR:n"
	Param     string `json:"param,omitempty"`

	// System variable identity (Kind "sv").
	SysVarID   int    `json:"sysvar_id,omitempty"`
	SysVarName string `json:"sysvar_name,omitempty"`

	Label    string    `json:"label"`
	Type     rega.Type `json:"type"`
	Format   Format    `json:"format"`
	Scale    float64   `json:"scale"` // register value = CCU value * Scale (integer formats)
	Writable bool      `json:"writable"`

	Area    table.Area `json:"area"`
	Address int        `json:"address"`

	Removed bool `json:"removed,omitempty"` // deselected: address reserved
	Orphan  bool `json:"orphan,omitempty"`  // object gone from the CCU
}

// File is the stored mapping.
type File struct {
	Schema    int     `json:"schema"`
	WordOrder string  `json:"word_order"` // "ABCD" (default) or "CDAB"
	Entries   []Entry `json:"entries"`
	// Next free address per area and the next export number; they only
	// grow, so an address is never handed out twice.
	Next       [4]int `json:"next"`
	NextExport int    `json:"next_export"`
}

// New returns an empty mapping. Input registers start after the status
// block (0..3).
func New(statusLen int) *File {
	f := &File{Schema: 1, WordOrder: "ABCD", NextExport: 1, Entries: []Entry{}}
	f.Next[table.InputRegisters] = statusLen
	return f
}

// KeyOfPoint returns the identity of a datapoint.
func KeyOfPoint(p rega.Point) string { return "dp:" + p.ID }

// KeyOfSysVar returns the identity of a system variable.
func KeyOfSysVar(v rega.SysVar) string { return fmt.Sprintf("sv:%d", v.ReGaID) }

// Load reads a mapping file; a missing file gives New(statusLen).
func Load(path string, statusLen int) (*File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(statusLen), nil
	}
	if err != nil {
		return nil, err
	}
	f := &File{}
	if err := json.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.Validate(statusLen); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Save writes the mapping atomically with mode 0600.
func (f *File) Save(path string) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// size returns the number of addresses an entry occupies.
func (e Entry) size() int {
	if e.Format == Bit {
		return 1
	}
	return e.Format.Words()
}

// Validate checks that entries do not overlap and stay below Next.
func (f *File) Validate(statusLen int) error {
	if f.Schema != 1 {
		return fmt.Errorf("unsupported mapping schema %d", f.Schema)
	}
	if f.WordOrder != "ABCD" && f.WordOrder != "CDAB" {
		return fmt.Errorf("invalid word order %q", f.WordOrder)
	}
	used := map[table.Area]map[int]string{}
	keys := map[string]bool{}
	exports := map[int]bool{}
	for _, e := range f.Entries {
		if e.Area > table.InputRegisters || !e.Format.Valid() {
			return fmt.Errorf("%s: invalid area/format", e.Key)
		}
		if (e.Format == Bit) != e.Area.IsBit() {
			return fmt.Errorf("%s: format %s does not fit %s", e.Key, e.Format, e.Area)
		}
		if e.Writable && !e.Area.AllowsWrite() {
			return fmt.Errorf("%s: writable in read-only area", e.Key)
		}
		if keys[e.Key] {
			return fmt.Errorf("%s: duplicate", e.Key)
		}
		keys[e.Key] = true
		if e.Export < 1 || e.Export > MaxExports || exports[e.Export] || e.Export >= f.NextExport {
			return fmt.Errorf("%s: invalid export number %d", e.Key, e.Export)
		}
		exports[e.Export] = true
		lo := 0
		if e.Area == table.InputRegisters {
			lo = statusLen
		}
		if e.Address < lo || e.Address+e.size() > f.Next[e.Area] {
			return fmt.Errorf("%s: address %d outside allocated range", e.Key, e.Address)
		}
		if e.Area == table.DiscreteInputs && f.Next[e.Area] > ValidBase {
			return fmt.Errorf("discrete inputs reach the status bit blocks")
		}
		if used[e.Area] == nil {
			used[e.Area] = map[int]string{}
		}
		for i := 0; i < e.size(); i++ {
			if other, ok := used[e.Area][e.Address+i]; ok {
				return fmt.Errorf("%s overlaps %s at %s %d", e.Key, other, e.Area, e.Address+i)
			}
			used[e.Area][e.Address+i] = e.Key
		}
	}
	return nil
}

// Find returns the entry for a key.
func (f *File) Find(key string) (*Entry, bool) {
	for i := range f.Entries {
		if f.Entries[i].Key == key {
			return &f.Entries[i], true
		}
	}
	return nil, false
}

// Active returns the entries that are exported (not removed), sorted by
// area and address.
func (f *File) Active() []Entry {
	var out []Entry
	for _, e := range f.Entries {
		if !e.Removed {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Area != out[j].Area {
			return out[i].Area < out[j].Area
		}
		return out[i].Address < out[j].Address
	})
	return out
}

// place sets the area for an entry from its format and writability and
// gives it an address: its previous address if it fits, else the next
// free one. e.Key, Format and Writable must be set.
func (f *File) place(e *Entry, prev *Entry) error {
	bit := e.Format == Bit
	switch {
	case bit && e.Writable:
		e.Area = table.Coils
	case bit:
		e.Area = table.DiscreteInputs
	case e.Writable:
		e.Area = table.HoldingRegisters
	default:
		e.Area = table.InputRegisters
	}
	if prev != nil && prev.Area == e.Area && prev.size() == e.size() {
		e.Address = prev.Address
		return nil
	}
	n := f.Next[e.Area]
	limit := table.MaxSlots
	if e.Area == table.DiscreteInputs {
		limit = ValidBase
	}
	if n+e.size() > limit {
		return fmt.Errorf("%s: address space full", e.Area)
	}
	e.Address = n
	f.Next[e.Area] = n + e.size()
	return nil
}

// Add exports an object, or changes format/writability/label of an
// exported one. A change of area or size moves the entry to a new address
// (the old one stays reserved); otherwise the address is kept. A removed
// entry is reactivated with its old address when it fits.
func (f *File) Add(e Entry) (*Entry, error) {
	if !e.Format.Valid() {
		return nil, fmt.Errorf("invalid format %q", e.Format)
	}
	if e.Format != Bit && e.Scale == 0 {
		e.Scale = 1
	}
	if old, ok := f.Find(e.Key); ok {
		prev := *old
		e.Export = old.Export
		if err := f.place(&e, &prev); err != nil {
			return nil, err
		}
		// If the entry moved, its old address simply stays a gap: Next
		// never shrinks, so it is never handed out again.
		e.Removed, e.Orphan = false, false
		*old = e
		return old, nil
	}
	if f.NextExport > MaxExports {
		return nil, fmt.Errorf("too many exports (max %d)", MaxExports)
	}
	if err := f.place(&e, nil); err != nil {
		return nil, err
	}
	e.Export = f.NextExport
	f.NextExport++
	f.Entries = append(f.Entries, e)
	return &f.Entries[len(f.Entries)-1], nil
}

// Remove deselects an object; its address stays reserved.
func (f *File) Remove(key string) bool {
	e, ok := f.Find(key)
	if !ok || e.Removed {
		return false
	}
	e.Removed = true
	return true
}

// Layout builds the Modbus table layout: value slots of all active
// entries (removed and orphaned entries are gaps; orphans still occupy
// their address and read as invalid), plus the valid/warning bit blocks.
func (f *File) Layout(statusSlots []table.Slot) table.Layout {
	var l table.Layout
	for a := table.Coils; a <= table.InputRegisters; a++ {
		l.Slots[a] = make([]table.Slot, f.Next[a])
	}
	copy(l.Slots[table.InputRegisters], statusSlots)
	maxExport := 0
	for _, e := range f.Entries {
		if e.Removed {
			continue
		}
		if e.Export > maxExport {
			maxExport = e.Export
		}
		w := e.Writable && !e.Orphan
		switch e.size() {
		case 1:
			l.Slots[e.Area][e.Address] = table.Slot{Kind: table.Single, Writable: w}
		case 2:
			l.Slots[e.Area][e.Address] = table.Slot{Kind: table.First, Writable: w}
			l.Slots[e.Area][e.Address+1] = table.Slot{Kind: table.Second, Writable: w}
		}
	}
	if maxExport > 0 {
		di := make([]table.Slot, WarnBase+maxExport+1)
		copy(di, l.Slots[table.DiscreteInputs])
		for _, e := range f.Entries {
			if !e.Removed {
				di[ValidBase+e.Export] = table.Slot{Kind: table.Single}
				di[WarnBase+e.Export] = table.Slot{Kind: table.Single}
			}
		}
		l.Slots[table.DiscreteInputs] = di
	}
	return l
}
