// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        cmd/ccu-modbusserverd/service_test.go
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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/engine"
	"github.com/WolfHenk/CCU-ModbusServer/internal/mapping"
	"github.com/WolfHenk/CCU-ModbusServer/internal/poller"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

type fakeCCU struct {
	mu      sync.Mutex
	catErr  error
	pingErr error
	loads   int
}

func (f *fakeCCU) LoadCatalog(ctx context.Context) (*rega.Catalog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.catErr != nil {
		return nil, f.catErr
	}
	return &rega.Catalog{Loaded: time.Now(), Devices: []rega.Device{{Address: "X1", Channels: []rega.Channel{}}}, SysVars: []rega.SysVar{}}, nil
}

func (f *fakeCCU) ReadValues(ctx context.Context, ids []int) (map[int]rega.Reading, error) {
	return map[int]rega.Reading{}, f.Ping(ctx)
}

func (f *fakeCCU) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeCCU) set(cat, ping error) {
	f.mu.Lock()
	f.catErr, f.pingErr = cat, ping
	f.mu.Unlock()
}

func flags(t *testing.T, tab *table.Table) uint16 {
	t.Helper()
	v, err := tab.Read(table.InputRegisters, statusFlags, 1)
	if err != nil {
		t.Fatal(err)
	}
	return v[0]
}

func wait(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestService(t *testing.T, f *fakeCCU) (*service, *table.Table) {
	t.Helper()
	tab, err := table.New(baseLayout())
	if err != nil {
		t.Fatal(err)
	}
	pol := poller.New(f, poller.Config{IdleInterval: 10 * time.Millisecond, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond})
	svc := &service{version: "test", src: f, pol: pol, tab: tab, refresh: 20 * time.Millisecond, retry: 10 * time.Millisecond}
	pol.OnCycle = func(map[int]rega.Reading, poller.Health) { svc.updateStatus() }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go pol.Run(ctx)
	go svc.catalogLoop(ctx)
	return svc, tab
}

func TestStatusBitFollowsCCU(t *testing.T) {
	f := &fakeCCU{}
	svc, tab := newTestService(t, f)
	wait(t, func() bool { return flags(t, tab)&1 == 1 })

	f.set(nil, errors.New("rega down"))
	wait(t, func() bool { return flags(t, tab)&1 == 0 })
	if svc.ccuOK() {
		t.Fatal("ccuOK while ping fails")
	}
	f.set(nil, nil)
	wait(t, func() bool { return flags(t, tab)&1 == 1 })
}

func TestNoCatalogMeansNotOK(t *testing.T) {
	f := &fakeCCU{catErr: errors.New("no catalog")}
	svc, tab := newTestService(t, f)
	wait(t, func() bool { return svc.pol.Health().OK })
	time.Sleep(30 * time.Millisecond)
	if flags(t, tab)&1 != 0 || svc.ccuOK() {
		t.Fatal("status ok without catalog")
	}
	// a failing refresh keeps the last good catalog
	f.set(nil, nil)
	wait(t, func() bool { return flags(t, tab)&1 == 1 })
	f.set(errors.New("refresh failed"), nil)
	wait(t, func() bool { svc.mu.RLock(); defer svc.mu.RUnlock(); return svc.catErr != "" })
	if !svc.ccuOK() {
		t.Fatal("refresh failure dropped the catalog")
	}
}

func TestAPI(t *testing.T) {
	f := &fakeCCU{}
	svc, tab := newTestService(t, f)
	wait(t, func() bool { return flags(t, tab)&1 == 1 })
	h := svc.api()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || !st.CCUOK || st.Version != "test" {
		t.Fatalf("status %s %v", rec.Body, err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/catalog", nil))
	var cat rega.Catalog
	if err := json.Unmarshal(rec.Body.Bytes(), &cat); err != nil || len(cat.Devices) != 1 {
		t.Fatalf("catalog %s %v", rec.Body, err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST allowed: %d", rec.Code)
	}
}

func TestLoadConfigDefaultsAndLimits(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "c.json")
		os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	c, err := loadConfig(write(`{"schema":1,"port":502}`))
	if err != nil || c.PollIntervalMS != 1000 || c.APIListen != "127.0.0.1:18711" || c.ReGaURL != rega.DefaultURL {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for _, bad := range []string{
		`{"schema":1,"port":0}`,
		`{"schema":2,"port":502}`,
		`{"schema":1,"port":502,"poll_interval_ms":100}`,
		`{"schema":1,"port":502,"poll_interval_ms":70000}`,
		`{"schema":1,"port":502,"api_listen":"0.0.0.0:18711"}`,
		`{"schema":1,"port":502,"api_listen":"18711"}`,
		`{"schema":1,"port":502,"max_connections":65}`,
		`{"schema":1,`,
	} {
		if _, err := loadConfig(write(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func fpp(v float64) *float64 { return &v }

func apiCatalog() *rega.Catalog {
	return &rega.Catalog{Devices: []rega.Device{{Interface: "HmIP-RF", Address: "T1", Channels: []rega.Channel{
		{Address: "T1:1", Name: "Bad Thermostat", Points: []rega.Point{
			{ReGaID: 21, ID: "HmIP-RF.T1:1.SET_POINT_TEMPERATURE", Param: "SET_POINT_TEMPERATURE", Type: rega.Float, Min: fpp(4.5), Max: fpp(30.5), Unit: "°C", Readable: true, Writable: true},
			{ReGaID: 25, ID: "HmIP-RF.T1:1.ACTUAL_TEMPERATURE", Param: "ACTUAL_TEMPERATURE", Type: rega.Float, Min: fpp(-40), Max: fpp(80), Unit: "°C", Readable: true},
			{ReGaID: 26, ID: "HmIP-RF.T1:1.INFO", Param: "INFO", Type: rega.String, Readable: true},
		}},
	}}}, SysVars: []rega.SysVar{{ReGaID: 950, ID: "SV:950", Name: "Anwesenheit", Type: rega.Bool, Writable: true}}}
}

func apiService(t *testing.T) (*service, string) {
	t.Helper()
	dir := t.TempDir()
	mp := filepath.Join(dir, "mapping.json")
	pol := poller.New(&fakeCCU{}, poller.Config{})
	eng, err := engine.New(mapping.New(statusLen), pol, nil, statusSlots(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := &service{version: "t", src: &fakeCCU{}, pol: pol, eng: eng, mapPath: mp}
	s.cat = apiCatalog()
	eng.SetCatalog(s.cat)
	return s, mp
}

func post(t *testing.T, h http.Handler, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/mapping", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestMappingAPI(t *testing.T) {
	s, mp := apiService(t)
	h := s.api()
	if code, body := post(t, h, `{"action":"add","key":"dp:HmIP-RF.T1:1.ACTUAL_TEMPERATURE"}`); code != 200 {
		t.Fatalf("add: %d %s", code, body)
	}
	if code, body := post(t, h, `{"action":"add","key":"dp:HmIP-RF.T1:1.SET_POINT_TEMPERATURE","label":"Soll Bad","writable":true}`); code != 200 {
		t.Fatalf("add writable: %d %s", code, body)
	}
	if code, body := post(t, h, `{"action":"add","key":"sv:950","writable":true}`); code != 200 {
		t.Fatalf("add sysvar: %d %s", code, body)
	}
	// saved atomically and reloadable
	m, err := mapping.Load(mp, statusLen)
	if err != nil || len(m.Entries) != 3 {
		t.Fatalf("saved mapping: %+v %v", m, err)
	}
	if m.Entries[0].Label != "Bad Thermostat" || m.Entries[1].Label != "Soll Bad" || m.Entries[2].Label != "Anwesenheit" {
		t.Fatalf("labels: %+v", m.Entries)
	}
	if m.Entries[0].Format != mapping.Float32 || m.Entries[2].Format != mapping.Bit {
		t.Fatalf("default formats: %+v", m.Entries)
	}
	bad := []string{
		`{"action":"add","key":"dp:HmIP-RF.T1:1.INFO"}`,                                             // string
		`{"action":"add","key":"dp:HmIP-RF.T1:1.ACTUAL_TEMPERATURE","writable":true}`,               // CCU read-only
		`{"action":"add","key":"dp:HmIP-RF.T1:1.ACTUAL_TEMPERATURE","format":"int16","scale":1000}`, // overflow
		`{"action":"add","key":"dp:nix"}`,
		`{"action":"remove","key":"dp:nix"}`,
		`{"action":"word_order","word_order":"BADC"}`,
		`{"action":"explode"}`,
		`{"action":"add","key":"sv:950","bogus":1}`, // unknown field
		`not json`,
	}
	for _, b := range bad {
		if code, body := post(t, h, b); code == 200 {
			t.Errorf("accepted %s: %s", b, body)
		}
	}
	if m2, _ := mapping.Load(mp, statusLen); len(m2.Entries) != 3 {
		t.Fatal("rejected change modified the saved mapping")
	}
	if code, _ := post(t, h, `{"action":"remove","key":"sv:950"}`); code != 200 {
		t.Fatal("remove")
	}
	if code, _ := post(t, h, `{"action":"word_order","word_order":"CDAB"}`); code != 200 {
		t.Fatal("word order")
	}
	m, _ = mapping.Load(mp, statusLen)
	if !m.Entries[2].Removed || m.WordOrder != "CDAB" {
		t.Fatalf("after remove/word order: %+v", m)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/sheet", nil))
	var sheet struct {
		WordOrder string            `json:"word_order"`
		Rows      []engine.SheetRow `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sheet); err != nil || len(sheet.Rows) != 2 || sheet.WordOrder != "CDAB" {
		t.Fatalf("sheet: %s %v", rec.Body, err)
	}
	r := sheet.Rows[0] // input register 4 sorts after holding? no: input before holding
	if r.Table != "Input Register" || r.Address != 4 || r.Classic != "300005" || r.FCRead != "04" || r.Access != "R" || r.Unit != "°C" || r.ValidBit != 10001 {
		t.Fatalf("row 0: %+v", r)
	}
	r = sheet.Rows[1]
	if r.Table != "Holding Register" || r.Access != "R/W" || r.FCWrite != "16" || r.Classic != "400001" {
		t.Fatalf("row 1: %+v", r)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/writelog", nil))
	if rec.Code != 200 {
		t.Fatalf("writelog %d", rec.Code)
	}
}
