// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        cmd/ccu-modbusserverd/service.go
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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/engine"
	"github.com/WolfHenk/CCU-ModbusServer/internal/mapping"
	"github.com/WolfHenk/CCU-ModbusServer/internal/poller"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// catalogSource is what the service needs to load the catalog.
type catalogSource interface {
	LoadCatalog(ctx context.Context) (*rega.Catalog, error)
}

// service ties the data source, the poller, the engine and the local
// API together.
type service struct {
	version string
	src     catalogSource
	pol     *poller.Poller
	refresh time.Duration // catalog refresh, default 5 min
	retry   time.Duration // first retry while no catalog, default 5 s
	started time.Time

	// Normal operation: the engine owns the table.
	eng     *engine.Engine
	mapPath string
	editMu  sync.Mutex                        // serializes mapping changes from the API
	save    func(*mapping.File, string) error // nil = (*mapping.File).Save

	// Demo mode / tests without engine.
	tab     *table.Table
	exports int

	// status writes the status block (flags, uptime).
	status func(flags uint16, uptime uint32)

	mu     sync.RWMutex
	cat    *rega.Catalog
	catErr string
	catAt  time.Time
}

// catalogLoop loads the catalog at start (retrying with backoff until it
// works) and then refreshes it periodically.
func (s *service) catalogLoop(ctx context.Context) {
	refresh := s.refresh
	if refresh <= 0 {
		refresh = 5 * time.Minute
	}
	first := s.retry
	if first <= 0 {
		first = 5 * time.Second
	}
	backoff := first
	for {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cat, err := s.src.LoadCatalog(cctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		wait := refresh
		s.mu.Lock()
		if err != nil {
			s.catErr = err.Error()
			if s.cat == nil {
				wait = backoff
				if backoff *= 2; backoff > time.Minute {
					backoff = time.Minute
				}
			}
			log.Printf("catalog: %v", err)
		} else {
			s.cat, s.catErr, s.catAt = cat, "", time.Now()
			backoff = first
		}
		s.mu.Unlock()
		if err == nil && s.eng != nil {
			s.eng.SetCatalog(cat)
		}
		s.updateStatus()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// ccuOK reports whether the CCU logic is reachable: a catalog exists and
// the last poll cycle (or ping) succeeded. It says nothing about the
// validity of single values; that is the job of the per-value quality.
func (s *service) ccuOK() bool {
	s.mu.RLock()
	loaded := s.cat != nil
	s.mu.RUnlock()
	return loaded && s.pol.Health().OK
}

// updateStatus writes the status block: bit 0 of the flags = CCU logic
// reachable, uptime in seconds.
func (s *service) updateStatus() {
	var flags uint16
	if s.ccuOK() {
		flags |= 1
	}
	var up uint32
	if !s.started.IsZero() {
		up = uint32(time.Since(s.started) / time.Second)
	}
	switch {
	case s.status != nil:
		s.status(flags, up)
	case s.tab != nil:
		s.tab.Update(func(w table.Writer) { w.Set(table.InputRegisters, statusFlags, flags) })
	}
}

// Status is the JSON answer of /api/status.
type Status struct {
	Version       string        `json:"version"`
	CCUOK         bool          `json:"ccu_ok"` // CCU logic reachable; not: all values valid
	Exports       int           `json:"exports"`
	CatalogLoaded time.Time     `json:"catalog_loaded"`
	CatalogError  string        `json:"catalog_error"`
	Poll          poller.Health `json:"poll"`
}

// apiError is the JSON error answer.
type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// api serves the local JSON interface for the add-on page. It listens on
// 127.0.0.1 only; the CGI pages check the WebUI session and forward.
func (s *service) api() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		st := Status{Version: s.version, Exports: s.exports, CatalogLoaded: s.catAt, CatalogError: s.catErr}
		s.mu.RUnlock()
		if s.eng != nil {
			st.Exports = s.eng.Exports()
		}
		st.Poll = s.pol.Health()
		st.CCUOK = s.ccuOK()
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /api/catalog", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		cat := s.cat
		s.mu.RUnlock()
		if cat == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{"Geräteliste noch nicht geladen"})
			return
		}
		writeJSON(w, http.StatusOK, cat)
	})
	if s.eng == nil {
		return mux
	}
	mux.HandleFunc("GET /api/mapping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.eng.Mapping())
	})
	mux.HandleFunc("GET /api/sheet", func(w http.ResponseWriter, r *http.Request) {
		m := s.eng.Mapping()
		writeJSON(w, http.StatusOK, map[string]any{
			"word_order": m.WordOrder,
			"rows":       s.eng.Sheet(time.Now()),
		})
	})
	mux.HandleFunc("GET /api/writelog", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.eng.WriteLog())
	})
	mux.HandleFunc("POST /api/mapping", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeChange(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{"ungültige Anfrage: " + err.Error()})
			return
		}
		m, err := s.applyChange(req)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, m)
	})
	return mux
}

// maxChangeBody limits a change request; a real one is a few hundred bytes.
const maxChangeBody = 16 << 10

// decodeChange reads exactly one JSON object and nothing else.
//
// Protection against foreign web pages (CSRF): the API listens on the
// loopback interface only; the browser never talks to it directly. The
// add-on CGI checks the WebUI session and forwards the request without
// the browser's Origin header. A request that still carries an Origin or
// a cross-site Sec-Fetch-Site header did not come through that path and
// is refused. Content-Type application/json cannot be sent by a plain
// HTML form, which closes the remaining gap for simple cross-site posts.
func decodeChange(w http.ResponseWriter, r *http.Request) (MappingChange, error) {
	var req MappingChange
	if r.Header.Get("Origin") != "" {
		return req, errors.New("Origin-Kopf nicht erlaubt")
	}
	if v := r.Header.Get("Sec-Fetch-Site"); v != "" && v != "same-origin" && v != "none" {
		return req, errors.New("seitenfremde Anfrage")
	}
	ct := r.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
		return req, errors.New("Content-Type muss application/json sein")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChangeBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return req, errors.New("Daten nach dem JSON-Objekt")
	}
	return req, nil
}

func (s *service) saveMapping(m *mapping.File) error {
	if s.save != nil {
		return s.save(m, s.mapPath)
	}
	return m.Save(s.mapPath)
}

// MappingChange is one change requested by the add-on page.
type MappingChange struct {
	Action    string  `json:"action"` // "add" (also changes an export), "remove", "word_order"
	Key       string  `json:"key,omitempty"`
	Label     string  `json:"label,omitempty"`
	Format    string  `json:"format,omitempty"` // empty = default for the type
	Scale     float64 `json:"scale,omitempty"`
	Writable  bool    `json:"writable,omitempty"`
	WordOrder string  `json:"word_order,omitempty"`
}

// findObject looks up a key in the catalog and returns a prepared entry
// plus the metadata for the checks.
func findObject(cat *rega.Catalog, key string) (mapping.Entry, *float64, *float64, int, bool, string, error) {
	if cat == nil {
		return mapping.Entry{}, nil, nil, 0, false, "", errors.New("Geräteliste noch nicht geladen")
	}
	for _, d := range cat.Devices {
		for _, ch := range d.Channels {
			for _, p := range ch.Points {
				if mapping.KeyOfPoint(p) == key {
					if !p.Readable && p.Type != rega.Action {
						return mapping.Entry{}, nil, nil, 0, false, "", errors.New("Datenpunkt ist nicht lesbar")
					}
					e := mapping.Entry{Key: key, Kind: "dp", Interface: d.Interface, Device: d.Address, Channel: ch.Address, Param: p.Param, Type: p.Type}
					return e, p.Min, p.Max, len(p.ValueList), p.Writable, ch.Name, nil
				}
			}
		}
	}
	for _, v := range cat.SysVars {
		if mapping.KeyOfSysVar(v) == key {
			e := mapping.Entry{Key: key, Kind: "sv", SysVarID: v.ReGaID, SysVarName: v.Name, Type: v.Type}
			return e, v.Min, v.Max, len(v.ValueList), v.Writable, v.Name, nil
		}
	}
	return mapping.Entry{}, nil, nil, 0, false, "", fmt.Errorf("Objekt %q nicht in der Geräteliste", key)
}

// applyChange validates a change, activates it in the engine and only
// then saves it atomically. If saving fails, the previous mapping is
// activated again, so the running state always equals the file.
// Any error leaves everything unchanged.
//
// Catalog ownership: s.cat is the service's copy for lookups; the engine
// keeps its own clone (SetCatalog clones), so neither can change the other.
func (s *service) applyChange(req MappingChange) (*mapping.File, error) {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	m := s.eng.Mapping()
	switch req.Action {
	case "add":
		s.mu.RLock()
		cat := s.cat
		s.mu.RUnlock()
		e, min, max, listLen, ccuWritable, name, err := findObject(cat, req.Key)
		if err != nil {
			return nil, err
		}
		if !e.Type.Offered() {
			return nil, fmt.Errorf("Typ %s wird nicht angeboten", e.Type)
		}
		f := mapping.Format(req.Format)
		if f == "" {
			var ok bool
			if f, ok = mapping.DefaultFormat(e.Type); !ok {
				return nil, fmt.Errorf("kein Format für Typ %s", e.Type)
			}
		}
		scale := req.Scale
		if scale == 0 && f != mapping.Bit {
			scale = 1
		}
		if err := mapping.CheckFormat(e.Type, f, scale, min, max, listLen); err != nil {
			return nil, err
		}
		if req.Writable && !ccuWritable {
			return nil, errors.New("die CCU erlaubt für dieses Objekt kein Schreiben")
		}
		if e.Type == rega.Action && !req.Writable {
			return nil, errors.New("ein Tastendruck ist nur schreibend sinnvoll")
		}
		e.Format, e.Scale, e.Writable = f, scale, req.Writable
		e.Label = req.Label
		if e.Label == "" {
			e.Label = name
		}
		if _, err := m.Add(e); err != nil {
			return nil, err
		}
	case "remove":
		if !m.Remove(req.Key) {
			return nil, fmt.Errorf("%q ist nicht freigegeben", req.Key)
		}
	case "word_order":
		if req.WordOrder != "ABCD" && req.WordOrder != "CDAB" {
			return nil, errors.New("Wortreihenfolge muss ABCD oder CDAB sein")
		}
		m.WordOrder = req.WordOrder
	default:
		return nil, fmt.Errorf("unbekannte Aktion %q", req.Action)
	}
	if err := m.Validate(statusLen); err != nil {
		return nil, err
	}
	prev := s.eng.Mapping()
	if err := s.eng.SetMapping(m); err != nil {
		return nil, err
	}
	if err := s.saveMapping(m); err != nil {
		if rerr := s.eng.SetMapping(prev); rerr != nil {
			log.Printf("api: rollback failed: %v", rerr)
		}
		return nil, fmt.Errorf("Speichern fehlgeschlagen: %w", err)
	}
	return s.eng.Mapping(), nil
}
