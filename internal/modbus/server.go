// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/modbus/server.go
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

// Package modbus implements a Modbus TCP server (MODBUS Messaging on TCP/IP
// Implementation Guide V1.0b, MODBUS Application Protocol V1.1b3) for the
// function codes 01, 02, 03, 04, 05, 06, 15 and 16.
package modbus

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// Exception is a Modbus exception code. A Backend may return one to make
// the server answer with exactly that code.
type Exception byte

const (
	ExIllegalFunction    Exception = 0x01
	ExIllegalDataAddress Exception = 0x02
	ExIllegalDataValue   Exception = 0x03
	ExServerDeviceFail   Exception = 0x04
	ExServerDeviceBusy   Exception = 0x06
)

func (e Exception) Error() string {
	switch e {
	case ExIllegalFunction:
		return "modbus exception 01: illegal function"
	case ExIllegalDataAddress:
		return "modbus exception 02: illegal data address"
	case ExIllegalDataValue:
		return "modbus exception 03: illegal data value"
	case ExServerDeviceFail:
		return "modbus exception 04: server device failure"
	case ExServerDeviceBusy:
		return "modbus exception 06: server device busy"
	}
	return "modbus exception"
}

// Backend carries out client writes. It is called only after the server
// has validated quantities, byte counts and addresses. The backend decides
// whether the value is accepted; returning an Exception sends that code,
// any other error sends exception 04. On success the backend is
// responsible for updating the table (the table always shows the value
// reported by the data source, not the value last written).
type Backend interface {
	WriteCoils(start int, values []bool) error
	WriteRegisters(start int, values []uint16) error
}

// Protocol limits from the Application Protocol Specification V1.1b3.
const (
	MaxReadBits       = 2000
	MaxReadRegisters  = 125
	MaxWriteBits      = 1968
	MaxWriteRegisters = 123
	maxPDU            = 253
	maxMBAPLength     = maxPDU + 1 // unit identifier + PDU
)

// Config controls the server.
type Config struct {
	// OnlyUnitID restricts answers to one unit identifier. Nil answers
	// every unit identifier (the identifier is not significant for a
	// device reached directly over TCP). Requests for other identifiers
	// get no response.
	OnlyUnitID *uint8
	// MaxConnections limits simultaneous clients; further connections are
	// closed immediately. 0 means 16.
	MaxConnections int
	// IdleTimeout closes a connection that sends no request. 0 means 120 s.
	IdleTimeout time.Duration
	// FrameTimeout limits the time between the first and last byte of one
	// request and the time to send a response. 0 means 5 s.
	FrameTimeout time.Duration
}

// Server is a Modbus TCP server bound to one table.
type Server struct {
	cfg     Config
	tab     *table.Table
	backend Backend

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// NewServer creates a server. backend may be nil; then every write is
// answered with exception 04.
func NewServer(cfg Config, tab *table.Table, backend Backend) *Server {
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 16
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 120 * time.Second
	}
	if cfg.FrameTimeout <= 0 {
		cfg.FrameTimeout = 5 * time.Second
	}
	return &Server{cfg: cfg, tab: tab, backend: backend, conns: map[net.Conn]struct{}{}}
}

// ErrServerClosed is returned by Serve after Close.
var ErrServerClosed = errors.New("modbus: server closed")

// Serve accepts connections until Close is called.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return ErrServerClosed
	}
	s.ln = ln
	s.mu.Unlock()

	for {
		c, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return ErrServerClosed
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		s.mu.Lock()
		if s.closed || len(s.conns) >= s.cfg.MaxConnections {
			s.mu.Unlock()
			c.Close()
			continue
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(c)
	}
}

// Close stops accepting, closes all connections and waits for them.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	if s.ln != nil {
		s.ln.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

// Connections returns the number of open client connections.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) handle(c net.Conn) {
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.wg.Done()
	}()

	r := bufio.NewReaderSize(c, 512)
	var hdr [7]byte
	pdu := make([]byte, maxPDU)
	out := make([]byte, 7+maxPDU)

	for {
		// Wait for the first byte of the next request.
		c.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
		if _, err := io.ReadFull(r, hdr[:1]); err != nil {
			return
		}
		// The rest of the request must follow promptly.
		c.SetReadDeadline(time.Now().Add(s.cfg.FrameTimeout))
		if _, err := io.ReadFull(r, hdr[1:]); err != nil {
			return
		}
		tid := binary.BigEndian.Uint16(hdr[0:2])
		pid := binary.BigEndian.Uint16(hdr[2:4])
		length := int(binary.BigEndian.Uint16(hdr[4:6]))
		unit := hdr[6]

		// A wrong protocol identifier or impossible length means the byte
		// stream is not Modbus or has lost synchronisation; nothing safe
		// can be answered, so the connection is closed.
		if pid != 0 || length < 2 || length > maxMBAPLength {
			return
		}
		req := pdu[:length-1]
		if _, err := io.ReadFull(r, req); err != nil {
			return
		}
		if s.cfg.OnlyUnitID != nil && unit != *s.cfg.OnlyUnitID {
			continue
		}

		resp := s.process(req, out[7:7])
		binary.BigEndian.PutUint16(out[0:2], tid)
		binary.BigEndian.PutUint16(out[2:4], 0)
		binary.BigEndian.PutUint16(out[4:6], uint16(len(resp)+1))
		out[6] = unit
		c.SetWriteDeadline(time.Now().Add(s.cfg.FrameTimeout))
		if _, err := c.Write(out[:7+len(resp)]); err != nil {
			return
		}
	}
}

func exception(dst []byte, fc byte, e Exception) []byte {
	return append(dst, fc|0x80, byte(e))
}

// process handles one request PDU and appends the response PDU to dst.
// Checks follow the order of the specification state diagrams: function
// code (01), quantity and byte count (03), address (02), execution (04).
func (s *Server) process(req []byte, dst []byte) []byte {
	fc := req[0]
	d := req[1:]
	switch fc {
	case 0x01, 0x02:
		area := table.Coils
		if fc == 0x02 {
			area = table.DiscreteInputs
		}
		if len(d) != 4 {
			return exception(dst, fc, ExIllegalDataValue)
		}
		start := int(binary.BigEndian.Uint16(d[0:2]))
		qty := int(binary.BigEndian.Uint16(d[2:4]))
		if qty < 1 || qty > MaxReadBits {
			return exception(dst, fc, ExIllegalDataValue)
		}
		vals, err := s.tab.Read(area, start, qty)
		if err != nil {
			return exception(dst, fc, ExIllegalDataAddress)
		}
		n := (qty + 7) / 8
		dst = append(dst, fc, byte(n))
		for i := 0; i < n; i++ {
			var b byte
			for j := 0; j < 8 && i*8+j < qty; j++ {
				if vals[i*8+j] != 0 {
					b |= 1 << j
				}
			}
			dst = append(dst, b)
		}
		return dst

	case 0x03, 0x04:
		area := table.HoldingRegisters
		if fc == 0x04 {
			area = table.InputRegisters
		}
		if len(d) != 4 {
			return exception(dst, fc, ExIllegalDataValue)
		}
		start := int(binary.BigEndian.Uint16(d[0:2]))
		qty := int(binary.BigEndian.Uint16(d[2:4]))
		if qty < 1 || qty > MaxReadRegisters {
			return exception(dst, fc, ExIllegalDataValue)
		}
		vals, err := s.tab.Read(area, start, qty)
		if err != nil {
			return exception(dst, fc, ExIllegalDataAddress)
		}
		dst = append(dst, fc, byte(2*qty))
		for _, v := range vals {
			dst = binary.BigEndian.AppendUint16(dst, v)
		}
		return dst

	case 0x05:
		if len(d) != 4 {
			return exception(dst, fc, ExIllegalDataValue)
		}
		addr := int(binary.BigEndian.Uint16(d[0:2]))
		v := binary.BigEndian.Uint16(d[2:4])
		if v != 0x0000 && v != 0xFF00 {
			return exception(dst, fc, ExIllegalDataValue)
		}
		if err := s.tab.CheckWrite(table.Coils, addr, 1); err != nil {
			return exception(dst, fc, ExIllegalDataAddress)
		}
		if e := s.writeCoils(addr, []bool{v == 0xFF00}); e != 0 {
			return exception(dst, fc, e)
		}
		return append(dst, req...)

	case 0x06:
		if len(d) != 4 {
			return exception(dst, fc, ExIllegalDataValue)
		}
		addr := int(binary.BigEndian.Uint16(d[0:2]))
		v := binary.BigEndian.Uint16(d[2:4])
		if err := s.tab.CheckWrite(table.HoldingRegisters, addr, 1); err != nil {
			return exception(dst, fc, ExIllegalDataAddress)
		}
		if e := s.writeRegisters(addr, []uint16{v}); e != 0 {
			return exception(dst, fc, e)
		}
		return append(dst, req...)

	case 0x0F:
		if len(d) < 5 {
			return exception(dst, fc, ExIllegalDataValue)
		}
		start := int(binary.BigEndian.Uint16(d[0:2]))
		qty := int(binary.BigEndian.Uint16(d[2:4]))
		bc := int(d[4])
		if qty < 1 || qty > MaxWriteBits || bc != (qty+7)/8 || len(d) != 5+bc {
			return exception(dst, fc, ExIllegalDataValue)
		}
		if err := s.tab.CheckWrite(table.Coils, start, qty); err != nil {
			return exception(dst, fc, ExIllegalDataAddress)
		}
		vals := make([]bool, qty)
		for i := range vals {
			vals[i] = d[5+i/8]&(1<<(i%8)) != 0
		}
		if e := s.writeCoils(start, vals); e != 0 {
			return exception(dst, fc, e)
		}
		return append(dst, req[:5]...)

	case 0x10:
		if len(d) < 5 {
			return exception(dst, fc, ExIllegalDataValue)
		}
		start := int(binary.BigEndian.Uint16(d[0:2]))
		qty := int(binary.BigEndian.Uint16(d[2:4]))
		bc := int(d[4])
		if qty < 1 || qty > MaxWriteRegisters || bc != 2*qty || len(d) != 5+bc {
			return exception(dst, fc, ExIllegalDataValue)
		}
		if err := s.tab.CheckWrite(table.HoldingRegisters, start, qty); err != nil {
			return exception(dst, fc, ExIllegalDataAddress)
		}
		vals := make([]uint16, qty)
		for i := range vals {
			vals[i] = binary.BigEndian.Uint16(d[5+2*i:])
		}
		if e := s.writeRegisters(start, vals); e != 0 {
			return exception(dst, fc, e)
		}
		return append(dst, req[:5]...)
	}
	return exception(dst, fc&0x7F, ExIllegalFunction)
}

func backendException(err error) Exception {
	if err == nil {
		return 0
	}
	var e Exception
	if errors.As(err, &e) && e != 0 {
		return e
	}
	return ExServerDeviceFail
}

func (s *Server) writeCoils(start int, vals []bool) Exception {
	if s.backend == nil {
		return ExServerDeviceFail
	}
	return backendException(s.backend.WriteCoils(start, vals))
}

func (s *Server) writeRegisters(start int, vals []uint16) Exception {
	if s.backend == nil {
		return ExServerDeviceFail
	}
	return backendException(s.backend.WriteRegisters(start, vals))
}
