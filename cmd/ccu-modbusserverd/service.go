// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        cmd/ccu-modbusserverd/service.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.2.0
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
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/poller"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// catalogSource is what the service needs to load the catalog.
type catalogSource interface {
	LoadCatalog(ctx context.Context) (*rega.Catalog, error)
}

// service ties the data source, the poller, the Modbus table and the
// local API together.
type service struct {
	version string
	src     catalogSource
	pol     *poller.Poller
	tab     *table.Table
	exports int
	refresh time.Duration // catalog refresh, default 5 min
	retry   time.Duration // first retry while no catalog, default 5 s

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

// ccuOK reports whether the CCU connection is healthy: a catalog exists
// and the last poll cycle succeeded.
func (s *service) ccuOK() bool {
	s.mu.RLock()
	loaded := s.cat != nil
	s.mu.RUnlock()
	return loaded && s.pol.Health().OK
}

// updateStatus writes bit 0 (CCU connection ok) of the status register.
func (s *service) updateStatus() {
	var flags uint16
	if s.ccuOK() {
		flags |= 1
	}
	s.tab.Update(func(w table.Writer) { w.Set(table.InputRegisters, statusFlags, flags) })
}

// Status is the JSON answer of /api/status.
type Status struct {
	Version       string        `json:"version"`
	CCUOK         bool          `json:"ccu_ok"`
	Exports       int           `json:"exports"`
	CatalogLoaded time.Time     `json:"catalog_loaded"`
	CatalogError  string        `json:"catalog_error"`
	Poll          poller.Health `json:"poll"`
}

// api serves the local JSON interface for the add-on page. It listens on
// 127.0.0.1 only; the CGI pages check the WebUI session and forward.
func (s *service) api() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		st := Status{Version: s.version, Exports: s.exports, CatalogLoaded: s.catAt, CatalogError: s.catErr}
		s.mu.RUnlock()
		st.Poll = s.pol.Health()
		st.CCUOK = s.ccuOK()
		write(w, st)
	})
	mux.HandleFunc("GET /api/catalog", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		cat := s.cat
		s.mu.RUnlock()
		if cat == nil {
			http.Error(w, "catalog not loaded yet", http.StatusServiceUnavailable)
			return
		}
		write(w, cat)
	})
	return mux
}
