// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/modbus/server_test.go
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

// Test numbers T1..T10 refer to the test matrix agreed in
// CLAUDE_CHATGPT_AUSTAUSCH.txt (entry ChatGPT 2026-11-11 01:22).

package modbus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// Test image:
//
//	coils      0..1999 writable
//	discrete   0..1999
//	holding    0..199 writable singles, 10-11 writable pair, 30 reserved, 40 read-only
//	input      0..199 singles, 20-21 pair, 30 reserved
func testLayout() table.Layout {
	var l table.Layout
	c := make([]table.Slot, 2000)
	d := make([]table.Slot, 2000)
	for i := range c {
		c[i] = table.Slot{Kind: table.Single, Writable: true}
		d[i] = table.Slot{Kind: table.Single}
	}
	h := make([]table.Slot, 200)
	in := make([]table.Slot, 200)
	for i := range h {
		h[i] = table.Slot{Kind: table.Single, Writable: true}
		in[i] = table.Slot{Kind: table.Single}
	}
	h[10] = table.Slot{Kind: table.First, Writable: true}
	h[11] = table.Slot{Kind: table.Second, Writable: true}
	h[30] = table.Slot{Kind: table.Reserved}
	h[40] = table.Slot{Kind: table.Single}
	in[20] = table.Slot{Kind: table.First}
	in[21] = table.Slot{Kind: table.Second}
	in[30] = table.Slot{Kind: table.Reserved}
	l.Slots[table.Coils] = c
	l.Slots[table.DiscreteInputs] = d
	l.Slots[table.HoldingRegisters] = h
	l.Slots[table.InputRegisters] = in
	return l
}

type recBackend struct {
	mu    sync.Mutex
	tab   *table.Table
	err   error
	calls int
}

func (b *recBackend) WriteCoils(_ net.Addr, start int, v []bool) error {
	b.mu.Lock()
	b.calls++
	err := b.err
	b.mu.Unlock()
	if err != nil {
		return err
	}
	return table.Loopback{T: b.tab}.WriteCoils(nil, start, v)
}

func (b *recBackend) WriteRegisters(_ net.Addr, start int, v []uint16) error {
	b.mu.Lock()
	b.calls++
	err := b.err
	b.mu.Unlock()
	if err != nil {
		return err
	}
	return table.Loopback{T: b.tab}.WriteRegisters(nil, start, v)
}

type fixture struct {
	srv  *Server
	tab  *table.Table
	be   *recBackend
	addr string
}

func start(t *testing.T, cfg Config) *fixture {
	t.Helper()
	tab, err := table.New(testLayout())
	if err != nil {
		t.Fatal(err)
	}
	be := &recBackend{tab: tab}
	srv := NewServer(cfg, tab, be)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &fixture{srv: srv, tab: tab, be: be, addr: ln.Addr().String()}
}

func (f *fixture) dial(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func frame(tid uint16, unit byte, pdu []byte) []byte {
	b := make([]byte, 7, 7+len(pdu))
	binary.BigEndian.PutUint16(b[0:], tid)
	binary.BigEndian.PutUint16(b[4:], uint16(len(pdu)+1))
	b[6] = unit
	return append(b, pdu...)
}

// readFrame reads one response. It returns the transaction id, unit and PDU.
func readFrame(c net.Conn, timeout time.Duration) (uint16, byte, []byte, error) {
	c.SetReadDeadline(time.Now().Add(timeout))
	var h [7]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return 0, 0, nil, err
	}
	if pid := binary.BigEndian.Uint16(h[2:]); pid != 0 {
		return 0, 0, nil, errors.New("response protocol id != 0")
	}
	n := int(binary.BigEndian.Uint16(h[4:]))
	p := make([]byte, n-1)
	if _, err := io.ReadFull(c, p); err != nil {
		return 0, 0, nil, err
	}
	return binary.BigEndian.Uint16(h[0:]), h[6], p, nil
}

func call(t *testing.T, c net.Conn, pdu []byte) []byte {
	t.Helper()
	if _, err := c.Write(frame(0x1234, 1, pdu)); err != nil {
		t.Fatal(err)
	}
	tid, unit, p, err := readFrame(c, 2*time.Second)
	if err != nil {
		t.Fatalf("pdu % x: %v", pdu, err)
	}
	if tid != 0x1234 || unit != 1 {
		t.Fatalf("tid/unit not mirrored: %04x/%d", tid, unit)
	}
	return p
}

func req(fc byte, a, b uint16, extra ...byte) []byte {
	p := []byte{fc, byte(a >> 8), byte(a), byte(b >> 8), byte(b)}
	return append(p, extra...)
}

func wantEx(t *testing.T, resp []byte, fc byte, ex Exception) {
	t.Helper()
	if len(resp) != 2 || resp[0] != fc|0x80 || resp[1] != byte(ex) {
		t.Fatalf("got % x, want exception %02x/%02x", resp, fc|0x80, byte(ex))
	}
}

// T1: FC01/02 limits and bit packing.
func TestT1ReadBits(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	// pattern: coil i set when i%3==0
	f.tab.Update(func(w table.Writer) {
		for i := 0; i < 2000; i++ {
			if i%3 == 0 {
				w.Set(table.Coils, i, 1)
				w.Set(table.DiscreteInputs, i, 1)
			}
		}
	})
	for _, fc := range []byte{1, 2} {
		r := call(t, c, req(fc, 0, 1))
		if !bytes.Equal(r, []byte{fc, 1, 1}) {
			t.Errorf("fc%d qty1: % x", fc, r)
		}
		r = call(t, c, req(fc, 0, 2000))
		if len(r) != 2+250 || r[1] != 250 {
			t.Fatalf("fc%d qty2000: len %d", fc, len(r))
		}
		for i := 0; i < 2000; i++ {
			got := r[2+i/8]&(1<<(i%8)) != 0
			if got != (i%3 == 0) {
				t.Fatalf("fc%d bit %d = %v", fc, i, got)
			}
		}
		// 10 bits from 0: bits 0,3,6,9 -> 0x49, 0x02; unused bits zero
		r = call(t, c, req(fc, 0, 10))
		if !bytes.Equal(r, []byte{fc, 2, 0x49, 0x02}) {
			t.Errorf("fc%d packing: % x", fc, r)
		}
		wantEx(t, call(t, c, req(fc, 0, 0)), fc, ExIllegalDataValue)
		wantEx(t, call(t, c, req(fc, 0, 2001)), fc, ExIllegalDataValue)
		wantEx(t, call(t, c, req(fc, 1999, 2)), fc, ExIllegalDataAddress)
	}
}

// T2: FC03/04 limits and byte order.
func TestT2ReadRegisters(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	f.tab.Update(func(w table.Writer) {
		for i := 0; i < 200; i++ {
			w.Set(table.HoldingRegisters, i, uint16(0x0100+i))
			w.Set(table.InputRegisters, i, uint16(0x0200+i))
		}
	})
	for _, fc := range []byte{3, 4} {
		base := uint16(0x0100)
		if fc == 4 {
			base = 0x0200
		}
		r := call(t, c, req(fc, 0, 1))
		if !bytes.Equal(r, []byte{fc, 2, byte(base >> 8), byte(base)}) {
			t.Errorf("fc%d qty1: % x", fc, r)
		}
		r = call(t, c, req(fc, 50, 125))
		if len(r) != 2+250 || r[1] != 250 {
			t.Fatalf("fc%d qty125: len %d", fc, len(r))
		}
		for i := 0; i < 125; i++ {
			if v := binary.BigEndian.Uint16(r[2+2*i:]); v != base+uint16(50+i) {
				t.Fatalf("fc%d reg %d = %04x", fc, 50+i, v)
			}
		}
		wantEx(t, call(t, c, req(fc, 0, 0)), fc, ExIllegalDataValue)
		wantEx(t, call(t, c, req(fc, 0, 126)), fc, ExIllegalDataValue)
	}
}

// T3: FC05 accepts only 0x0000 and 0xFF00 and echoes the request.
func TestT3WriteSingleCoil(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	on := req(5, 7, 0xFF00)
	if r := call(t, c, on); !bytes.Equal(r, on) {
		t.Fatalf("echo: % x", r)
	}
	if r := call(t, c, req(1, 7, 1)); !bytes.Equal(r, []byte{1, 1, 1}) {
		t.Fatalf("readback on: % x", r)
	}
	off := req(5, 7, 0x0000)
	if r := call(t, c, off); !bytes.Equal(r, off) {
		t.Fatalf("echo off: % x", r)
	}
	if r := call(t, c, req(1, 7, 1)); !bytes.Equal(r, []byte{1, 1, 0}) {
		t.Fatalf("readback off: % x", r)
	}
	for _, v := range []uint16{0x0001, 0x1234, 0xFFFF, 0x00FF} {
		wantEx(t, call(t, c, req(5, 7, v)), 5, ExIllegalDataValue)
	}
	wantEx(t, call(t, c, req(5, 2000, 0xFF00)), 5, ExIllegalDataAddress)
}

// T4: FC06 only on complete single registers.
func TestT4WriteSingleRegister(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	w := req(6, 5, 0xBEEF)
	if r := call(t, c, w); !bytes.Equal(r, w) {
		t.Fatalf("echo: % x", r)
	}
	if r := call(t, c, req(3, 5, 1)); !bytes.Equal(r, []byte{3, 2, 0xBE, 0xEF}) {
		t.Fatalf("readback: % x", r)
	}
	wantEx(t, call(t, c, req(6, 10, 1)), 6, ExIllegalDataAddress) // first half
	wantEx(t, call(t, c, req(6, 11, 1)), 6, ExIllegalDataAddress) // second half
	wantEx(t, call(t, c, req(6, 30, 1)), 6, ExIllegalDataAddress) // reserved
	wantEx(t, call(t, c, req(6, 40, 1)), 6, ExIllegalDataAddress) // read-only
	wantEx(t, call(t, c, req(6, 200, 1)), 6, ExIllegalDataAddress)
}

// T5: FC15 limits, byte count and response.
func TestT5WriteMultipleCoils(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	// 10 coils from 100: 0b11_0000_0101 -> bytes 0x05, 0x03; unused bits set to 1 must be ignored
	r := call(t, c, req(15, 100, 10, 2, 0x05, 0xFF))
	if !bytes.Equal(r, req(15, 100, 10)) {
		t.Fatalf("response: % x", r)
	}
	r = call(t, c, req(1, 100, 10))
	if !bytes.Equal(r, []byte{1, 2, 0x05, 0x03}) {
		t.Fatalf("readback: % x", r)
	}
	// 1 and 1968
	if r := call(t, c, req(15, 0, 1, 1, 1)); !bytes.Equal(r, req(15, 0, 1)) {
		t.Fatalf("qty1: % x", r)
	}
	big := append(req(15, 0, 1968, 246), make([]byte, 246)...)
	if r := call(t, c, big); !bytes.Equal(r, req(15, 0, 1968)) {
		t.Fatalf("qty1968: % x", r)
	}
	wantEx(t, call(t, c, req(15, 0, 0, 0)), 15, ExIllegalDataValue)
	tooBig := append(req(15, 0, 1969, 247), make([]byte, 247)...)
	wantEx(t, call(t, c, tooBig), 15, ExIllegalDataValue)
	wantEx(t, call(t, c, req(15, 0, 10, 1, 0)), 15, ExIllegalDataValue)       // byte count too small
	wantEx(t, call(t, c, req(15, 0, 10, 2, 0)), 15, ExIllegalDataValue)       // data shorter than byte count
	wantEx(t, call(t, c, req(15, 0, 10, 2, 0, 0, 0)), 15, ExIllegalDataValue) // data longer than byte count
	wantEx(t, call(t, c, req(15, 1995, 10, 2, 0, 0)), 15, ExIllegalDataAddress)
}

// T6: FC16 limits, byte count, 32-bit halves and response.
func TestT6WriteMultipleRegisters(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	r := call(t, c, req(16, 9, 3, 6, 0, 1, 0x41, 0xAC, 0, 0))
	if !bytes.Equal(r, req(16, 9, 3)) {
		t.Fatalf("response: % x", r)
	}
	r = call(t, c, req(3, 9, 3))
	if !bytes.Equal(r, []byte{3, 6, 0, 1, 0x41, 0xAC, 0, 0}) {
		t.Fatalf("readback: % x", r)
	}
	if r := call(t, c, req(16, 0, 1, 2, 0, 9)); !bytes.Equal(r, req(16, 0, 1)) {
		t.Fatalf("qty1: % x", r)
	}
	// 123 registers 50..172 contain no pair, gap or read-only slot? 40 is read-only -> use 41..163
	big := append(req(16, 41, 123, 246), make([]byte, 246)...)
	if r := call(t, c, big); !bytes.Equal(r, req(16, 41, 123)) {
		t.Fatalf("qty123: % x", r)
	}
	wantEx(t, call(t, c, req(16, 0, 0, 0)), 16, ExIllegalDataValue)
	// 124 registers cannot be sent with full data: the PDU would have 254
	// bytes (> 253). Quantity 124 is rejected with 03 by itself.
	wantEx(t, call(t, c, req(16, 41, 124, 248, 0, 0)), 16, ExIllegalDataValue)
	wantEx(t, call(t, c, req(16, 0, 2, 3, 0, 0, 0)), 16, ExIllegalDataValue)
	wantEx(t, call(t, c, req(16, 0, 2, 4, 0, 0)), 16, ExIllegalDataValue)
	wantEx(t, call(t, c, req(16, 10, 1, 2, 0, 0)), 16, ExIllegalDataAddress)       // first half only
	wantEx(t, call(t, c, req(16, 11, 2, 4, 0, 0, 0, 0)), 16, ExIllegalDataAddress) // starts at second half
	wantEx(t, call(t, c, req(16, 29, 2, 4, 0, 0, 0, 0)), 16, ExIllegalDataAddress) // covers gap
	// rejected write must not change anything
	if r := call(t, c, req(3, 9, 3)); !bytes.Equal(r, []byte{3, 6, 0, 1, 0x41, 0xAC, 0, 0}) {
		t.Fatalf("changed by rejected write: % x", r)
	}
}

// T7: illegal function, addresses, gaps.
func TestT7FunctionAndAddress(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	for _, fc := range []byte{0x07, 0x08, 0x11, 0x17, 0x2B, 0x7F} {
		wantEx(t, call(t, c, []byte{fc, 0, 0}), fc, ExIllegalFunction)
	}
	wantEx(t, call(t, c, req(4, 200, 1)), 4, ExIllegalDataAddress)
	wantEx(t, call(t, c, req(4, 190, 11)), 4, ExIllegalDataAddress)
	wantEx(t, call(t, c, req(4, 21, 1)), 4, ExIllegalDataAddress) // second half
	wantEx(t, call(t, c, req(4, 19, 2)), 4, ExIllegalDataAddress) // ends in first half
	if r := call(t, c, req(4, 20, 2)); len(r) != 6 {
		t.Fatalf("pair read: % x", r)
	}
	f.tab.Update(func(w table.Writer) { w.Set(table.InputRegisters, 29, 5, 6, 7) })
	if r := call(t, c, req(4, 29, 3)); !bytes.Equal(r, []byte{4, 6, 0, 5, 0, 0, 0, 7}) {
		t.Fatalf("gap must read 0: % x", r)
	}
	// wrong PDU length for reads
	wantEx(t, call(t, c, []byte{3, 0, 0, 0}), 3, ExIllegalDataValue)
	wantEx(t, call(t, c, []byte{3, 0, 0, 0, 1, 0}), 3, ExIllegalDataValue)
}

// T8: MBAP handling.
func TestT8MBAP(t *testing.T) {
	f := start(t, Config{})

	t.Run("transaction id mirrored", func(t *testing.T) {
		c := f.dial(t)
		for _, tid := range []uint16{0, 1, 0xABCD, 0xFFFF} {
			c.Write(frame(tid, 7, req(3, 0, 1)))
			got, unit, _, err := readFrame(c, time.Second)
			if err != nil || got != tid || unit != 7 {
				t.Fatalf("tid %04x -> %04x unit %d err %v", tid, got, unit, err)
			}
		}
	})

	closed := func(t *testing.T, raw []byte) {
		t.Helper()
		c := f.dial(t)
		c.Write(raw)
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var b [1]byte
		n, err := c.Read(b[:])
		if n != 0 || err == nil || errors.Is(err, errTimeout(err)) {
			t.Fatalf("connection not closed: n=%d err=%v", n, err)
		}
	}
	t.Run("protocol id != 0", func(t *testing.T) {
		raw := frame(1, 1, req(3, 0, 1))
		raw[3] = 1
		closed(t, raw)
	})
	t.Run("length 1", func(t *testing.T) {
		closed(t, []byte{0, 1, 0, 0, 0, 1, 1})
	})
	t.Run("length 255", func(t *testing.T) {
		closed(t, []byte{0, 1, 0, 0, 0, 255, 1})
	})
	t.Run("length 0", func(t *testing.T) {
		closed(t, []byte{0, 1, 0, 0, 0, 0, 1})
	})

	t.Run("fragmented", func(t *testing.T) {
		c := f.dial(t)
		raw := frame(9, 1, req(3, 0, 2))
		for _, b := range raw {
			c.Write([]byte{b})
			time.Sleep(5 * time.Millisecond)
		}
		tid, _, p, err := readFrame(c, time.Second)
		if err != nil || tid != 9 || len(p) != 6 {
			t.Fatalf("tid %d p % x err %v", tid, p, err)
		}
	})

	t.Run("two frames in one write", func(t *testing.T) {
		c := f.dial(t)
		raw := append(frame(1, 1, req(3, 0, 1)), frame(2, 1, req(4, 0, 1))...)
		c.Write(raw)
		for want := uint16(1); want <= 2; want++ {
			tid, _, p, err := readFrame(c, time.Second)
			if err != nil || tid != want {
				t.Fatalf("frame %d: tid %d err %v", want, tid, err)
			}
			if p[0] != byte(2+want) {
				t.Fatalf("frame %d answered fc %d", want, p[0])
			}
		}
	})

	t.Run("EOF inside frame", func(t *testing.T) {
		c := f.dial(t)
		raw := frame(1, 1, req(3, 0, 1))
		c.Write(raw[:9])
		c.Close()
		// server must keep serving others
		c2 := f.dial(t)
		if r := call(t, c2, req(3, 0, 1)); len(r) != 4 {
			t.Fatalf("after EOF: % x", r)
		}
	})

	t.Run("max length frame", func(t *testing.T) {
		c := f.dial(t)
		// FC16 with 123 registers = PDU of 252 bytes, length field 253
		big := append(req(16, 41, 123, 246), make([]byte, 246)...)
		if r := call(t, c, big); !bytes.Equal(r, req(16, 41, 123)) {
			t.Fatalf("% x", r)
		}
		// 253-byte PDU of an unknown function is still a valid frame
		pdu := make([]byte, 253)
		pdu[0] = 0x64
		wantEx(t, call(t, c, pdu), 0x64, ExIllegalFunction)
	})
}

// errTimeout returns a sentinel matching timeout errors, so that a
// read that merely timed out is not mistaken for a closed connection.
func errTimeout(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return err
	}
	return errNever
}

var errNever = errors.New("never")

// T9: address + quantity overflow.
func TestT9Overflow(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	wantEx(t, call(t, c, req(3, 0xFFFF, 2)), 3, ExIllegalDataAddress)
	wantEx(t, call(t, c, req(1, 0xFFFF, 2000)), 1, ExIllegalDataAddress)
	wantEx(t, call(t, c, req(16, 0xFFFF, 2, 4, 0, 0, 0, 0)), 16, ExIllegalDataAddress)
	wantEx(t, call(t, c, req(15, 0xFFFF, 2, 1, 0)), 15, ExIllegalDataAddress)
}

// T10: parallel clients see consistent 32-bit values; a stalled client
// does not block others.
func TestT10Concurrency(t *testing.T) {
	f := start(t, Config{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for v := uint16(0); ; v++ {
			select {
			case <-stop:
				return
			default:
			}
			f.tab.Update(func(w table.Writer) { w.Set(table.InputRegisters, 20, v, ^v) })
		}
	}()
	// stalled client: half a header, never completed
	stalled := f.dial(t)
	stalled.Write([]byte{0, 1, 0})

	var bad atomic.Int32
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			c, err := net.Dial("tcp", f.addr)
			if err != nil {
				bad.Add(1)
				return
			}
			defer c.Close()
			for n := 0; n < 300; n++ {
				c.Write(frame(uint16(n), 1, req(4, 20, 2)))
				_, _, p, err := readFrame(c, 2*time.Second)
				if err != nil || len(p) != 6 {
					bad.Add(1)
					return
				}
				a := binary.BigEndian.Uint16(p[2:])
				b := binary.BigEndian.Uint16(p[4:])
				if b != ^a {
					bad.Add(1)
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	wg.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("%d inconsistent or failed reads", n)
	}
}

// Unit identifier: default answers all, OnlyUnitID answers one.
func TestUnitID(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	for _, u := range []byte{0, 1, 17, 255} {
		c.Write(frame(1, u, req(3, 0, 1)))
		if _, unit, _, err := readFrame(c, time.Second); err != nil || unit != u {
			t.Fatalf("unit %d: got %d err %v", u, unit, err)
		}
	}
	one := uint8(1)
	g := start(t, Config{OnlyUnitID: &one})
	c2 := g.dial(t)
	c2.Write(frame(1, 5, req(3, 0, 1)))
	if _, _, _, err := readFrame(c2, 300*time.Millisecond); err == nil {
		t.Fatal("unit 5 answered")
	}
	if r := call(t, c2, req(3, 0, 1)); len(r) != 4 {
		t.Fatalf("unit 1: % x", r)
	}
}

// Backend errors map to exceptions 04 and 06; validation happens before
// the backend is called.
func TestBackendErrors(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	f.be.mu.Lock()
	f.be.err = errors.New("ccu refused")
	f.be.mu.Unlock()
	wantEx(t, call(t, c, req(6, 0, 1)), 6, ExServerDeviceFail)
	f.be.mu.Lock()
	f.be.err = ExServerDeviceBusy
	f.be.mu.Unlock()
	wantEx(t, call(t, c, req(5, 0, 0xFF00)), 5, ExServerDeviceBusy)
	f.be.mu.Lock()
	before := f.be.calls
	f.be.mu.Unlock()
	wantEx(t, call(t, c, req(16, 10, 1, 2, 0, 0)), 16, ExIllegalDataAddress)
	wantEx(t, call(t, c, req(5, 0, 0x1234)), 5, ExIllegalDataValue)
	f.be.mu.Lock()
	after := f.be.calls
	f.be.mu.Unlock()
	if after != before {
		t.Fatalf("backend called for invalid requests (%d -> %d)", before, after)
	}
}

func TestMaxConnections(t *testing.T) {
	f := start(t, Config{MaxConnections: 2})
	a := f.dial(t)
	b := f.dial(t)
	call(t, a, req(3, 0, 1))
	call(t, b, req(3, 0, 1))
	c := f.dial(t)
	c.Write(frame(1, 1, req(3, 0, 1)))
	if _, _, _, err := readFrame(c, 500*time.Millisecond); err == nil {
		t.Fatal("third connection answered")
	}
	a.Close()
	deadline := time.Now().Add(2 * time.Second)
	for f.srv.Connections() > 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	d := f.dial(t)
	if r := call(t, d, req(3, 0, 1)); len(r) != 4 {
		t.Fatalf("after close: % x", r)
	}
}

func TestIdleTimeout(t *testing.T) {
	f := start(t, Config{IdleTimeout: 200 * time.Millisecond})
	c := f.dial(t)
	time.Sleep(400 * time.Millisecond)
	c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("idle connection still open")
	}
}

func TestNoBackend(t *testing.T) {
	tab, _ := table.New(testLayout())
	s := NewServer(Config{}, tab, nil)
	r := s.process(nil, req(6, 0, 1), nil)
	wantEx(t, r, 6, ExServerDeviceFail)
}

func TestSetTable(t *testing.T) {
	f := start(t, Config{})
	c := f.dial(t)
	f.tab.Update(func(w table.Writer) { w.Set(table.InputRegisters, 0, 7) })
	if r := call(t, c, req(4, 0, 1)); !bytes.Equal(r, []byte{4, 2, 0, 7}) {
		t.Fatalf("old table: % x", r)
	}
	var l table.Layout
	l.Slots[table.InputRegisters] = []table.Slot{{Kind: table.Single}}
	nt, _ := table.New(l)
	nt.Update(func(w table.Writer) { w.Set(table.InputRegisters, 0, 9) })
	f.srv.SetTable(nt)
	if r := call(t, c, req(4, 0, 1)); !bytes.Equal(r, []byte{4, 2, 0, 9}) {
		t.Fatalf("new table: % x", r)
	}
	wantEx(t, call(t, c, req(4, 1, 1)), 4, ExIllegalDataAddress)
	if f.srv.Table() != nt {
		t.Fatal("Table()")
	}
}
