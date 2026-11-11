// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/engine/engine.go
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

// Package engine connects the catalog, the mapping, the poller and the
// Modbus table: it resolves mapping entries to CCU objects, tells the
// poller what to read, and writes values, valid bits and warning bits
// into the table after every poll cycle.
package engine

import (
	"strconv"
	"sync"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/mapping"
	"github.com/WolfHenk/CCU-ModbusServer/internal/poller"
	"github.com/WolfHenk/CCU-ModbusServer/internal/quality"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// Poller is the part of the poller the engine uses.
type Poller interface {
	SetItems(ids []int)
	Values() (map[int]rega.Reading, poller.Health)
}

// TableSink receives a rebuilt table (the Modbus server).
type TableSink interface {
	SetTable(t *table.Table)
}

// object is what the engine knows about one mapping entry.
type object struct {
	entry   mapping.Entry
	regaID  int // 0 = not found in the catalog (orphan)
	unreach int // ReGa ID of the device's UNREACH, 0 = none
	meta    meta
}

// meta is the CCU metadata needed for write checks.
type meta struct {
	typ       rega.Type
	min, max  *float64
	listLen   int
	writable  bool // CCU allows writing
	pointName string
}

// Engine is safe for concurrent use.
type Engine struct {
	StatusSlots []table.Slot
	MaxAge      time.Duration // value invalid if the last good read is older

	pol  Poller
	sink TableSink
	rec  *quality.Reception

	// rebuildMu serializes complete rebuilds including their external
	// effects (poller selection, table hand-over), so a later change can
	// never be overtaken by an earlier one.
	rebuildMu sync.Mutex

	mu      sync.Mutex
	m       *mapping.File
	cat     *rega.Catalog
	objs    []object        // active entries
	byAddr  map[addrKey]int // area+address -> index into objs
	tab     *table.Table
	last    map[string]lastWords // last good words per entry key
	started time.Time
}

// lastWords is a substitute value together with the representation it
// was encoded in; after a change of format, scale, type or word order it
// is not used any more.
type lastWords struct {
	sig   string
	words []uint16
}

func signature(en mapping.Entry, wordOrder string) string {
	return string(en.Type) + "|" + string(en.Format) + "|" + strconv.FormatFloat(en.Scale, 'g', -1, 64) + "|" + wordOrder
}

type addrKey struct {
	area table.Area
	addr int
}

// New creates an engine for a mapping. The mapping is copied and
// validated.
func New(m *mapping.File, pol Poller, sink TableSink, statusSlots []table.Slot, maxAge time.Duration) (*Engine, error) {
	if err := m.Validate(len(statusSlots)); err != nil {
		return nil, err
	}
	e := &Engine{
		StatusSlots: statusSlots, MaxAge: maxAge,
		pol: pol, sink: sink, rec: quality.NewReception(),
		m: m.Clone(), last: map[string]lastWords{}, started: time.Now(),
	}
	e.rebuildMu.Lock()
	defer e.rebuildMu.Unlock()
	e.rebuild()
	return e, nil
}

// Reception returns the reception tracker (for the API).
func (e *Engine) Reception() *quality.Reception { return e.rec }

// Table returns the current table.
func (e *Engine) Table() *table.Table {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tab
}

// SetCatalog resolves the entries against a new catalog.
func (e *Engine) SetCatalog(cat *rega.Catalog) {
	e.rebuildMu.Lock()
	defer e.rebuildMu.Unlock()
	e.mu.Lock()
	e.cat = cat
	e.mu.Unlock()
	e.rebuild()
}

// SetMapping replaces the mapping (after a change in the WebUI). The
// mapping is copied and validated first; on error nothing changes.
func (e *Engine) SetMapping(m *mapping.File) error {
	c := m.Clone()
	if err := c.Validate(len(e.StatusSlots)); err != nil {
		return err
	}
	e.rebuildMu.Lock()
	defer e.rebuildMu.Unlock()
	e.mu.Lock()
	e.m = c
	e.mu.Unlock()
	e.rebuild()
	return nil
}

// Mapping returns a copy of the current mapping.
func (e *Engine) Mapping() *mapping.File {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.m.Clone()
}

// resolve finds the CCU object of an entry. A system variable is found
// by its ReGa ID, or - after a restore with new IDs - by its name.
func resolve(cat *rega.Catalog, en mapping.Entry) (int, meta, bool) {
	if cat == nil {
		return 0, meta{}, false
	}
	if en.Kind == "sv" {
		var byName *rega.SysVar
		for i := range cat.SysVars {
			v := &cat.SysVars[i]
			if v.ReGaID == en.SysVarID {
				return v.ReGaID, svMeta(v), true
			}
			if en.SysVarName != "" && v.Name == en.SysVarName {
				byName = v
			}
		}
		if byName != nil {
			return byName.ReGaID, svMeta(byName), true
		}
		return 0, meta{}, false
	}
	for _, d := range cat.Devices {
		for _, ch := range d.Channels {
			for _, p := range ch.Points {
				if mapping.KeyOfPoint(p) == en.Key {
					return p.ReGaID, meta{typ: p.Type, min: p.Min, max: p.Max, listLen: len(p.ValueList), writable: p.Writable, pointName: p.ID}, true
				}
			}
		}
	}
	return 0, meta{}, false
}

func svMeta(v *rega.SysVar) meta {
	return meta{typ: v.Type, min: v.Min, max: v.Max, listLen: len(v.ValueList), writable: v.Writable, pointName: v.Name}
}

// rebuild resolves all entries, rebuilds the table and the poll list.
func (e *Engine) rebuild() {
	e.mu.Lock()
	unreach := quality.UnreachIDs(e.cat)
	var objs []object
	var ids []int
	// orphan state is computed here, never stored in the file
	work := *e.m
	work.Entries = append([]mapping.Entry(nil), e.m.Entries...)
	for i := range work.Entries {
		en := &work.Entries[i]
		en.Orphan = false
		if en.Removed {
			continue
		}
		id, md, ok := resolve(e.cat, *en)
		if ok && mapping.CheckFormat(md.typ, en.Format, en.Scale, md.min, md.max, md.listLen) != nil {
			ok = false // type changed in the CCU: format no longer fits
		}
		if ok && en.Writable && !md.writable {
			ok = false // CCU no longer allows writing
		}
		if !ok {
			en.Orphan = true
			id = 0
		}
		objs = append(objs, object{entry: *en, regaID: id, unreach: unreach[id], meta: md})
		if id != 0 {
			ids = append(ids, id)
		}
	}
	byAddr := map[addrKey]int{}
	for i, o := range objs {
		byAddr[addrKey{o.entry.Area, o.entry.Address}] = i
	}
	tab, err := table.New(work.Layout(e.StatusSlots))
	if err != nil {
		// A validated mapping always gives a valid layout; keep the old
		// table rather than serving nothing.
		e.mu.Unlock()
		return
	}
	// The new table is complete BEFORE anybody sees it: status block
	// carried over, values and bits filled from the current readings.
	if e.tab != nil {
		if v, err := e.tab.Read(table.InputRegisters, 0, len(e.StatusSlots)); err == nil {
			tab.Update(func(w table.Writer) { w.Set(table.InputRegisters, 0, v...) })
		}
	}
	values, h := e.pol.Values()
	e.fill(tab, objs, work.WordOrder, values, h, time.Now())
	e.objs, e.byAddr, e.tab = objs, byAddr, tab
	keep := map[string]bool{}
	for _, o := range objs {
		keep[o.entry.Key] = true
	}
	for k := range e.last {
		if !keep[k] {
			delete(e.last, k)
		}
	}
	e.mu.Unlock()

	// External effects, still under rebuildMu.
	e.pol.SetItems(quality.PollIDs(ids, unreach))
	if e.sink != nil {
		e.sink.SetTable(tab)
	}
}

// Exports returns the number of active entries.
func (e *Engine) Exports() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.objs)
}

// OnCycle is the poller callback.
func (e *Engine) OnCycle(values map[int]rega.Reading, _ poller.Health) {
	if values != nil {
		e.rec.Observe(values)
	}
	e.Refresh(time.Now())
}

// Refresh writes values and status bits of all entries into the table.
// It is called after every poll cycle and once per second, so that the
// valid bits drop when the CCU logic stops answering.
func (e *Engine) Refresh(now time.Time) {
	values, h := e.pol.Values()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fill(e.tab, e.objs, e.m.WordOrder, values, h, now)
}

// fill writes values and bits of objs into tab. e.mu must be held.
func (e *Engine) fill(tab *table.Table, objs []object, wordOrder string, values map[int]rega.Reading, h poller.Health, now time.Time) {
	tab.Update(func(w table.Writer) {
		for _, o := range objs {
			en := o.entry
			sig := signature(en, wordOrder)
			if l, ok := e.last[en.Key]; ok && l.sig != sig {
				delete(e.last, en.Key) // representation changed: old words are meaningless
			}
			reason := quality.Missing
			if o.regaID != 0 {
				reason = quality.Judge(o.regaID, values, h.LastOK, e.MaxAge, now)
			}
			valid := false
			var words []uint16
			if reason.Valid() {
				var ok bool
				words, ok = mapping.Encode(en, wordOrder, values[o.regaID].Value)
				if ok {
					valid = true
					e.last[en.Key] = lastWords{sig: sig, words: words}
				}
			}
			if !valid {
				// substitute: float32 NaN, otherwise the last good value
				// in the same representation (0 before the first one)
				switch {
				case en.Format == mapping.Float32:
					words = mapping.NaNWords(wordOrder)
				case e.last[en.Key].words != nil:
					words = e.last[en.Key].words
				default:
					words = make([]uint16, max(1, en.Format.Words()))
				}
			}
			if en.Type == rega.Action {
				words = []uint16{0} // actions always read 0
			}
			w.Set(en.Area, en.Address, words...)
			var vb, wb uint16
			if valid {
				vb = 1
			}
			if o.regaID != 0 && quality.CommState(o.unreach, values).Warning() {
				wb = 1
			}
			w.Set(table.DiscreteInputs, mapping.ValidBase+en.Export, vb)
			w.Set(table.DiscreteInputs, mapping.WarnBase+en.Export, wb)
		}
	})
}

// SetStatus writes the status block (flags, uptime, exports).
func (e *Engine) SetStatus(flags uint16, uptime uint32) {
	e.mu.Lock()
	tab, n := e.tab, len(e.objs)
	e.mu.Unlock()
	tab.Update(func(w table.Writer) {
		w.Set(table.InputRegisters, 0, flags, uint16(uptime>>16), uint16(uptime), uint16(n))
	})
}
