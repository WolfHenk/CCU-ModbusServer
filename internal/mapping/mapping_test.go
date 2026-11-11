// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/mapping/mapping_test.go
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

package mapping

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

const statusLen = 4

// fill gives test entries a kind and a type that fit the format.
func fill(e Entry) Entry {
	if e.Kind == "" {
		e.Kind = "dp"
	}
	if e.Type == "" {
		switch e.Format {
		case Bit:
			e.Type = rega.Bool
		case Float32:
			e.Type = rega.Float
		default:
			e.Type = rega.Integer
		}
	}
	return e
}

func mustAdd(t *testing.T, f *File, e Entry) *Entry {
	t.Helper()
	got, err := f.Add(fill(e))
	if err != nil {
		t.Fatalf("add %s: %v", e.Key, err)
	}
	return got
}

func TestAllocationIsStable(t *testing.T) {
	f := New(statusLen)
	a := *mustAdd(t, f, Entry{Key: "dp:A", Format: Float32})             // IR 4-5
	b := *mustAdd(t, f, Entry{Key: "dp:B", Format: Uint16})              // IR 6
	c := *mustAdd(t, f, Entry{Key: "dp:C", Format: Bit})                 // DI 0
	d := *mustAdd(t, f, Entry{Key: "dp:D", Format: Bit, Writable: true}) // Coil 0
	h := *mustAdd(t, f, Entry{Key: "dp:H", Format: Int32, Writable: true})
	if a.Area != table.InputRegisters || a.Address != 4 || b.Address != 6 {
		t.Fatalf("IR: %+v %+v", a, b)
	}
	if c.Area != table.DiscreteInputs || c.Address != 0 || d.Area != table.Coils || d.Address != 0 {
		t.Fatalf("bits: %+v %+v", c, d)
	}
	if h.Area != table.HoldingRegisters || h.Address != 0 || h.Scale != 1 {
		t.Fatalf("holding: %+v", h)
	}
	if a.Export != 1 || h.Export != 5 {
		t.Fatalf("export numbers %d %d", a.Export, h.Export)
	}

	// deselect B: gap stays, next new object is appended
	if !f.Remove("dp:B") {
		t.Fatal("remove")
	}
	e := *mustAdd(t, f, Entry{Key: "dp:E", Format: Uint16})
	if e.Address != 7 {
		t.Fatalf("new entry reused a gap: %+v", e)
	}
	// reselect B: old address back
	b2 := *mustAdd(t, f, Entry{Key: "dp:B", Format: Uint16})
	if b2.Address != 6 || b2.Export != b.Export {
		t.Fatalf("reselect: %+v", b2)
	}
	// B now float32: does not fit 1 register -> moves to the end
	b3 := *mustAdd(t, f, Entry{Key: "dp:B", Format: Float32})
	if b3.Address != 8 || b3.Export != b.Export {
		t.Fatalf("resize: %+v", b3)
	}
	// label change keeps the address
	a2 := *mustAdd(t, f, Entry{Key: "dp:A", Format: Float32, Label: "neu"})
	if a2.Address != 4 || a2.Label != "neu" {
		t.Fatalf("label change: %+v", a2)
	}
	// enabling write moves A from input to holding registers
	a3 := *mustAdd(t, f, Entry{Key: "dp:A", Format: Float32, Writable: true})
	if a3.Area != table.HoldingRegisters || a3.Address != 2 {
		t.Fatalf("write enable: %+v", a3)
	}
	if err := f.Validate(statusLen); err != nil {
		t.Fatal(err)
	}
}

func TestLayout(t *testing.T) {
	f := New(statusLen)
	mustAdd(t, f, Entry{Key: "dp:A", Format: Float32})
	mustAdd(t, f, Entry{Key: "dp:B", Format: Uint16})
	mustAdd(t, f, Entry{Key: "dp:C", Format: Bit})
	mustAdd(t, f, Entry{Key: "dp:D", Format: Int16, Writable: true})
	f.Remove("dp:B")
	status := []table.Slot{{Kind: table.Single}, {Kind: table.First}, {Kind: table.Second}, {Kind: table.Single}}
	l := f.Layout(status)
	if _, err := table.New(l); err != nil {
		t.Fatal(err)
	}
	in := l.Slots[table.InputRegisters]
	if len(in) != 7 || in[4].Kind != table.First || in[5].Kind != table.Second || in[6].Kind != table.Reserved {
		t.Fatalf("input registers %+v", in)
	}
	if !l.Slots[table.HoldingRegisters][0].Writable {
		t.Fatal("holding not writable")
	}
	di := l.Slots[table.DiscreteInputs]
	if di[0].Kind != table.Single || di[ValidBase+1].Kind != table.Single || di[WarnBase+3].Kind != table.Single {
		t.Fatal("status bits missing")
	}
	if di[ValidBase+2].Kind != table.Reserved {
		t.Fatal("removed entry still has a valid bit")
	}
	if len(di) != WarnBase+4+1 {
		t.Fatalf("di length %d", len(di))
	}
}

func TestOrphanNotWritable(t *testing.T) {
	f := New(statusLen)
	e := mustAdd(t, f, Entry{Key: "dp:A", Format: Int16, Writable: true})
	e.Orphan = true
	l := f.Layout(nil)
	if l.Slots[table.HoldingRegisters][0].Writable || l.Slots[table.HoldingRegisters][0].Kind != table.Single {
		t.Fatalf("orphan: %+v", l.Slots[table.HoldingRegisters][0])
	}
}

func TestSaveLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mapping.json")
	f, err := Load(p, statusLen)
	if err != nil || len(f.Entries) != 0 || f.Next[table.InputRegisters] != statusLen {
		t.Fatalf("missing file: %+v %v", f, err)
	}
	mustAdd(t, f, Entry{Key: "dp:A", Format: Float32, Label: "Außen"})
	mustAdd(t, f, Entry{Key: "sv:950", Kind: "sv", SysVarID: 950, Type: rega.Bool, Format: Bit, Writable: true})
	if err := f.Save(p); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	g, err := Load(p, statusLen)
	if err != nil || len(g.Entries) != 2 || g.Entries[0].Label != "Außen" || g.NextExport != 3 {
		t.Fatalf("reload: %+v %v", g, err)
	}
	bad := []func(*File){
		func(f *File) {
			f.Entries[1].Address = 4
			f.Entries[1].Area = table.InputRegisters
			f.Entries[1].Format = Uint16
			f.Entries[1].Writable = false
		}, // overlap
		func(f *File) { f.Entries[1].Key = f.Entries[0].Key },
		func(f *File) { f.Entries[0].Address = 100 },
		func(f *File) { f.Entries[0].Address = 2 }, // inside status block
		func(f *File) { f.Entries[1].Export = 1 },
		func(f *File) { f.Entries[0].Format = Bit },
		func(f *File) { f.Entries[0].Writable = true },
		func(f *File) { f.WordOrder = "BADC" },
		func(f *File) { f.Schema = 2 },
		// Regression M4: manipulated counters must never reach Layout
		func(f *File) { f.Next[table.HoldingRegisters] = -1 },
		func(f *File) { f.Next[table.HoldingRegisters] = table.MaxSlots + 1 },
		func(f *File) { f.Next[table.DiscreteInputs] = ValidBase + 1 },
		func(f *File) { f.Next[table.InputRegisters] = 2 }, // inside status block
		func(f *File) { f.NextExport = 0 },
		func(f *File) { f.NextExport = MaxExports + 2 },
		func(f *File) { f.Entries[0].Kind = "sv" },    // key/kind mismatch
		func(f *File) { f.Entries[1].SysVarID = 951 }, // key does not match id
		func(f *File) { f.Entries[0].Kind = "xx" },
		func(f *File) { f.Entries[0].Type = rega.String }, // not offered
		func(f *File) { f.Entries[0].Type = rega.Bool },   // bool as float32
		func(f *File) { f.Entries[0].Scale = 10 },         // float32 with scale
		func(f *File) { f.Entries[0].Key = "dp:" },
	}
	for i, mod := range bad {
		g, _ := Load(p, statusLen)
		mod(g)
		if err := g.Validate(statusLen); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	os.WriteFile(p, []byte("{"), 0o600)
	if _, err := Load(p, statusLen); err == nil {
		t.Error("broken file accepted")
	}
}

func TestLimits(t *testing.T) {
	f := New(statusLen)
	f.Next[table.DiscreteInputs] = ValidBase - 1
	mustAdd(t, f, Entry{Key: "dp:last", Format: Bit})
	if _, err := f.Add(fill(Entry{Key: "dp:full", Format: Bit})); err == nil {
		t.Fatal("discrete inputs ran into the status bit block")
	}
	g := New(statusLen)
	g.NextExport = MaxExports + 1
	if _, err := g.Add(fill(Entry{Key: "dp:x", Format: Uint16})); err == nil {
		t.Fatal("export limit not enforced")
	}
	if _, err := g.Add(fill(Entry{Key: "dp:y", Format: "int8"})); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestAddRejectsInconsistentEntries(t *testing.T) {
	f := New(statusLen)
	for _, e := range []Entry{
		{Key: "dp:a", Kind: "dp", Type: rega.Float, Format: Int16, Scale: -1},
		{Key: "dp:b", Kind: "dp", Type: rega.Float, Format: Int16, Scale: math.Inf(1)},
		{Key: "dp:c", Kind: "dp", Type: rega.Integer, Format: Int32, Scale: math.NaN()},
		{Key: "dp:d", Kind: "dp", Type: rega.Action, Format: Bit},            // read-only action
		{Key: "sv:1", Kind: "sv", SysVarID: 2, Type: rega.Bool, Format: Bit}, // key mismatch
		{Key: "x", Kind: "dp", Type: rega.Bool, Format: Bit},
	} {
		if _, err := f.Add(e); err == nil {
			t.Errorf("accepted %+v", e)
		}
	}
	if len(f.Entries) != 0 || f.Next[table.InputRegisters] != statusLen || f.NextExport != 1 {
		t.Fatalf("rejected entries changed the mapping: %+v", f)
	}
}

func TestCloneIsDeep(t *testing.T) {
	f := New(statusLen)
	mustAdd(t, f, Entry{Key: "dp:A", Format: Uint16})
	c := f.Clone()
	c.Entries[0].Label = "x"
	c.Next[table.InputRegisters] = 99
	if f.Entries[0].Label == "x" || f.Next[table.InputRegisters] == 99 {
		t.Fatal("clone shares state")
	}
}
