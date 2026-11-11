// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        cmd/ccu-modbusserverd/main.go
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

// Command ccu-modbusserverd provides CCU values to Modbus TCP clients.
//
// Stage 1 (scaffold): serves the status block and, with -demo, a fixed
// test image in all four tables. The CCU connection follows in stage 2.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/modbus"
	"github.com/WolfHenk/CCU-ModbusServer/internal/poller"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/source"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

var version = "dev"

// Config is the add-on configuration file.
type Config struct {
	Schema         int    `json:"schema"`
	Port           int    `json:"port"`
	OnlyUnitID     *uint8 `json:"only_unit_id"`
	MaxConnections int    `json:"max_connections"`
	ReGaURL        string `json:"rega_url"`
	PollIntervalMS int    `json:"poll_interval_ms"`
	APIListen      string `json:"api_listen"`
}

func loadConfig(path string) (Config, error) {
	c := Config{Schema: 1, Port: 502, ReGaURL: rega.DefaultURL, PollIntervalMS: 1000, APIListen: "127.0.0.1:18711"}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Schema != 1 {
		return c, fmt.Errorf("%s: unsupported schema %d", path, c.Schema)
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("%s: invalid port %d", path, c.Port)
	}
	if c.MaxConnections < 0 || c.MaxConnections > 64 {
		return c, fmt.Errorf("%s: max_connections must be 0..64", path)
	}
	if c.PollIntervalMS < 200 || c.PollIntervalMS > 60000 {
		return c, fmt.Errorf("%s: poll_interval_ms must be 200..60000", path)
	}
	if host, _, err := net.SplitHostPort(c.APIListen); err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return c, fmt.Errorf("%s: api_listen must be a loopback address", path)
	}
	return c, nil
}

// Status block at the start of the input registers.
const (
	statusFlags   = 0 // bit 0: CCU connection ok
	statusUptime  = 1 // uint32, seconds since start (registers 1-2)
	statusExports = 3 // number of exported values
	statusLen     = 4
)

func statusSlots() []table.Slot {
	return []table.Slot{
		{Kind: table.Single},
		{Kind: table.First}, {Kind: table.Second},
		{Kind: table.Single},
	}
}

func single(w bool) table.Slot { return table.Slot{Kind: table.Single, Writable: w} }

func pair(w bool) []table.Slot {
	return []table.Slot{{Kind: table.First, Writable: w}, {Kind: table.Second, Writable: w}}
}

// demoLayout is the fixed stage-1 test image:
//
//	Coils             0-3  writable
//	Discrete Inputs   0-3  read only (0 and 2 set)
//	Holding Registers 0    writable int16, 1-2 writable float32, 3 reserved, 4 writable
//	Input Registers   0-3  status block, 4-5 float32 21.5, 6 reserved, 7 uint16 1234
func demoLayout() table.Layout {
	var l table.Layout
	l.Slots[table.Coils] = []table.Slot{single(true), single(true), single(true), single(true)}
	l.Slots[table.DiscreteInputs] = []table.Slot{single(false), single(false), single(false), single(false)}
	h := []table.Slot{single(true)}
	h = append(h, pair(true)...)
	h = append(h, table.Slot{Kind: table.Reserved}, single(true))
	l.Slots[table.HoldingRegisters] = h
	in := statusSlots()
	in = append(in, pair(false)...)
	in = append(in, table.Slot{Kind: table.Reserved}, single(false))
	l.Slots[table.InputRegisters] = in
	return l
}

func baseLayout() table.Layout {
	var l table.Layout
	l.Slots[table.InputRegisters] = statusSlots()
	return l
}

func float32Regs(f float32) (uint16, uint16) {
	b := math.Float32bits(f)
	return uint16(b >> 16), uint16(b)
}

// runBenchPoll measures one bundled ReGa value read of n objects, 20
// times in a row without overlap, and prints the timings.
func runBenchPoll(cfg Config, n int) error {
	ctx := context.Background()
	c := rega.NewClient(cfg.ReGaURL)
	cat, err := c.LoadCatalog(ctx)
	if err != nil {
		return err
	}
	var base []int
	for _, d := range cat.Devices {
		for _, ch := range d.Channels {
			for _, p := range ch.Points {
				base = append(base, p.ReGaID)
			}
		}
	}
	for _, v := range cat.SysVars {
		base = append(base, v.ReGaID)
	}
	if len(base) == 0 {
		return fmt.Errorf("no objects")
	}
	ids := make([]int, n)
	for i := range ids {
		ids[i] = base[i%len(base)]
	}
	var min, max, sum time.Duration
	const runs = 20
	for i := 0; i < runs; i++ {
		t := time.Now()
		if _, err := c.ReadValues(ctx, ids); err != nil {
			return err
		}
		d := time.Since(t)
		if i == 0 || d < min {
			min = d
		}
		if d > max {
			max = d
		}
		sum += d
	}
	fmt.Printf("objects=%d distinct=%d runs=%d min=%v avg=%v max=%v\n", n, len(base), runs, min, sum/runs, max)
	return nil
}

// probeServer sends one status block read to the local server.
func probeServer(cfg Config) error {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(cfg.Port), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	unit := byte(1)
	if cfg.OnlyUnitID != nil {
		unit = *cfg.OnlyUnitID
	}
	req := []byte{0x00, 0x01, 0, 0, 0, 6, unit, 0x04, 0, statusFlags, 0, 1}
	if _, err := c.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 11)
	if _, err := io.ReadFull(c, resp); err != nil {
		return err
	}
	// MBAP: transaction 1, protocol 0, length 5 (unit + fc + count + 2),
	// same unit; PDU: FC04 with one register. This is a liveness check of
	// the local server, not a protocol test.
	want := []byte{0x00, 0x01, 0, 0, 0, 5, unit, 0x04, 2}
	for i, b := range want {
		if resp[i] != b {
			return fmt.Errorf("unexpected response % x", resp)
		}
	}
	return nil
}

func main() {
	cfgPath := flag.String("config", "/usr/local/etc/config/addons/ccu-modbusserver/config.json", "configuration file")
	check := flag.Bool("check", false, "check configuration and that the port is free, then exit")
	demo := flag.Bool("demo", false, "serve the fixed stage-1 test image")
	listen := flag.String("listen", "", "listen address, overrides the configured port (tests)")
	printPort := flag.Bool("print-port", false, "print the configured port and exit (used by the start script)")
	dumpCatalog := flag.Bool("dump-catalog", false, "read the CCU catalog from ReGa, print it as JSON and exit")
	benchPoll := flag.Int("bench-poll", 0, "measure ReGa value polling with N objects (IDs repeated), then exit")
	probe := flag.Bool("probe", false, "check that a running server answers on 127.0.0.1:<port>, then exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	log.SetFlags(log.LstdFlags)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	if *printPort {
		fmt.Println(cfg.Port)
		return
	}
	if *dumpCatalog {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cat, err := rega.NewClient(cfg.ReGaURL).LoadCatalog(ctx)
		if err != nil {
			log.Fatalf("catalog: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cat)
		return
	}
	if *benchPoll > 0 {
		if err := runBenchPoll(cfg, *benchPoll); err != nil {
			log.Fatalf("bench: %v", err)
		}
		return
	}
	if *probe {
		if err := probeServer(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "probe:", err)
			os.Exit(1)
		}
		fmt.Println("ok")
		return
	}
	addr := *listen
	if addr == "" {
		addr = ":" + strconv.Itoa(cfg.Port)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			log.Fatalf("Port %s ist bereits belegt (anderes Programm). Bitte anderen Port wählen.", addr)
		}
		log.Fatalf("listen %s: %v", addr, err)
	}
	if *check {
		ln.Close()
		fmt.Println("ok")
		return
	}

	layout := baseLayout()
	if *demo {
		layout = demoLayout()
	}
	tab, err := table.New(layout)
	if err != nil {
		log.Fatalf("layout: %v", err)
	}
	exports := tab.Count(table.Coils) + tab.Count(table.DiscreteInputs) +
		tab.Count(table.HoldingRegisters) + tab.Count(table.InputRegisters) - 3 // without status block
	tab.Update(func(w table.Writer) {
		w.Set(table.InputRegisters, statusExports, uint16(exports))
		if *demo {
			w.Set(table.DiscreteInputs, 0, 1, 0, 1, 0)
			hi, lo := float32Regs(21.5)
			w.Set(table.InputRegisters, 4, hi, lo)
			w.Set(table.InputRegisters, 7, 1234)
		}
	})

	srv := modbus.NewServer(modbus.Config{
		OnlyUnitID:     cfg.OnlyUnitID,
		MaxConnections: cfg.MaxConnections,
	}, tab, table.Loopback{T: tab})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ReGa is the only data source today (see package source).
	var ds source.DataSource = rega.NewClient(cfg.ReGaURL)
	pol := poller.New(ds, poller.Config{Interval: time.Duration(cfg.PollIntervalMS) * time.Millisecond})
	svc := &service{version: version, src: ds, pol: pol, tab: tab, exports: exports}
	pol.OnCycle = func(map[int]rega.Reading, poller.Health) { svc.updateStatus() }
	go pol.Run(ctx)
	go svc.catalogLoop(ctx)

	apiLn, err := net.Listen("tcp", cfg.APIListen)
	if err != nil {
		log.Fatalf("api listen %s: %v", cfg.APIListen, err)
	}
	apiSrv := &http.Server{Handler: svc.api(), ReadHeaderTimeout: 5 * time.Second}
	go apiSrv.Serve(apiLn)

	started := time.Now()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				up := uint32(time.Since(started) / time.Second)
				tab.Update(func(w table.Writer) {
					w.Set(table.InputRegisters, statusUptime, uint16(up>>16), uint16(up))
				})
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		cancel()
		apiSrv.Close()
		srv.Close()
	}()

	log.Printf("ccu-modbusserverd %s listening on %s (demo=%v, exports=%d)", version, ln.Addr(), *demo, exports)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, modbus.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
	log.Printf("stopped")
}
