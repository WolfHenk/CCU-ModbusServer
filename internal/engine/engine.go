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
	"fmt"
	"log"
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
	// ReadAt is the start of the cycle that read the current value of id.
	ReadAt(id int) time.Time
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
	list      []string
	unit      string
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

	mu     sync.Mutex
	m      *mapping.File
	cat    *rega.Catalog
	objs   []object        // active entries
	byAddr map[addrKey]int // area+address -> index into objs
	tab    *table.Table
	last   map[string]lastWords // last good words per entry key
	// idKey remembers which entry a ReGa ID last belonged to (kept across
	// rebuilds, so A -> gone -> B is noticed too). stale holds IDs whose
	// cached reading may belong to a different object, with the time of
	// the rebuild that reassigned them. Such an ID counts as "not read
	// yet" until the poller has read THIS ID in a cycle that started
	// after that rebuild (ReadAt per ID); other cycles do not clear it.
	idKey map[int]string
	stale map[int]time.Time

	// write path
	wsrc      ValueSetter
	wcfg      WriteConfig
	lastWrite map[string]time.Time
	wgen      uint64 // changes with every rebuild and SetWriter
	wlog      []LogEntry
	started   time.Time
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
		last: map[string]lastWords{}, started: time.Now(),
		lastWrite: map[string]time.Time{}, idKey: map[int]string{}, stale: map[int]time.Time{},
	}
	e.rebuildMu.Lock()
	defer e.rebuildMu.Unlock()
	if err := e.rebuild(m.Clone(), nil); err != nil {
		return nil, err
	}
	return e, nil
}

// SetSink sets the receiver of rebuilt tables (the Modbus server, which
// itself needs the engine as backend, hence after New).
func (e *Engine) SetSink(s TableSink) {
	e.rebuildMu.Lock()
	defer e.rebuildMu.Unlock()
	e.mu.Lock()
	e.sink = s
	tab := e.tab
	e.mu.Unlock()
	if s != nil {
		s.SetTable(tab)
	}
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
	cat = cat.Clone() // the caller may keep and change its catalog
	e.rebuildMu.Lock()
	defer e.rebuildMu.Unlock()
	e.mu.Lock()
	m := e.m
	e.mu.Unlock()
	if err := e.rebuild(m, cat); err != nil {
		log.Printf("engine: catalog not applied: %v", err)
	}
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
	cat := e.cat
	e.mu.Unlock()
	// The mapping becomes active only if the rebuild succeeds.
	return e.rebuild(c, cat)
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
					return p.ReGaID, meta{typ: p.Type, min: p.Min, max: p.Max, listLen: len(p.ValueList), list: p.ValueList, unit: p.Unit, writable: p.Writable, pointName: p.ID}, true
				}
			}
		}
	}
	return 0, meta{}, false
}

func svMeta(v *rega.SysVar) meta {
	return meta{typ: v.Type, min: v.Min, max: v.Max, listLen: len(v.ValueList), list: v.ValueList, unit: v.Unit, writable: v.Writable, pointName: v.Name}
}

// rebuild resolves the entries of m against cat, builds and fills the
// new table and only then makes m, cat and the table current. On error
// nothing changes. e.rebuildMu must be held; m and cat are owned by the
// engine from here on.
func (e *Engine) rebuild(m *mapping.File, cat *rega.Catalog) error {
	unreach := quality.UnreachIDs(cat)
	var objs []object
	var ids []int
	// orphan state is computed here, never stored in the file
	work := m.Clone()
	for i := range work.Entries {
		en := &work.Entries[i]
		en.Orphan = false
		if en.Removed {
			continue
		}
		id, md, ok := resolve(cat, *en)
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
		return fmt.Errorf("table layout: %w", err)
	}
	idKey := map[int]string{}
	for _, o := range objs {
		if o.regaID != 0 {
			idKey[o.regaID] = o.entry.Key
		}
	}

	e.mu.Lock()
	// IDs that now belong to a different entry than before: their cached
	// reading is not trusted until read again after this moment.
	at := time.Now()
	for id, k := range idKey {
		if old, ok := e.idKey[id]; ok && old != k {
			e.stale[id] = at
		}
	}
	for id, k := range e.idKey {
		if _, ok := idKey[id]; !ok {
			idKey[id] = k // keep the history of IDs no longer exported
		}
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
	e.m, e.cat = m, cat
	e.objs, e.byAddr, e.tab, e.idKey = objs, byAddr, tab, idKey
	e.wgen++ // running writes must not continue with the old exports
	keep := map[string]bool{}
	for _, o := range objs {
		keep[o.entry.Key] = true
	}
	for k := range e.last {
		if !keep[k] {
			delete(e.last, k)
		}
	}
	sink := e.sink
	e.mu.Unlock()

	// External effects, still under rebuildMu.
	e.pol.SetItems(quality.PollIDs(ids, unreach))
	if sink != nil {
		sink.SetTable(tab)
	}
	return nil
}

// Exports returns the number of active entries.
func (e *Engine) Exports() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.objs)
}

// isStale reports whether the cached reading of id may still belong to
// the object the ID had before the last reassignment. e.mu must be held.
func (e *Engine) isStale(id int) bool {
	at, ok := e.stale[id]
	if !ok {
		return false
	}
	if e.pol.ReadAt(id).After(at) {
		delete(e.stale, id) // read again after the reassignment
		return false
	}
	return true
}

// CheckMapping tells whether SetMapping(m) would be accepted, without
// changing anything. The API uses it to persist only mappings that can
// be activated.
func (e *Engine) CheckMapping(m *mapping.File) error {
	c := m.Clone()
	if err := c.Validate(len(e.StatusSlots)); err != nil {
		return err
	}
	if _, err := table.New(c.Layout(e.StatusSlots)); err != nil {
		return fmt.Errorf("table layout: %w", err)
	}
	return nil
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
				if e.isStale(o.regaID) {
					reason = quality.Unknown
				}
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
