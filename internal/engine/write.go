// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/engine/write.go
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

package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/mapping"
	"github.com/WolfHenk/CCU-ModbusServer/internal/modbus"
	"github.com/WolfHenk/CCU-ModbusServer/internal/quality"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// ValueSetter is the part of the data source used for writes.
type ValueSetter interface {
	SetValue(ctx context.Context, id int, t rega.Type, v float64) error
}

// WriteConfig controls the write path.
type WriteConfig struct {
	// Allow lists the client networks that may write. Empty means nobody
	// may write (mandatory as soon as anything is writable).
	Allow []*net.IPNet
	// MinInterval is the minimum time between two forwarded writes of
	// the same entry; default 1 s (radio duty cycle).
	MinInterval time.Duration
	// Timeout for one SetValue; default 5 s.
	Timeout time.Duration
}

// ParseAllow parses "192.168.1.10, 192.168.2.0/24" into networks.
func ParseAllow(list []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, s := range list {
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("Schreibfreigabe: %q ist weder IP-Adresse noch Netz", s)
		}
		out = append(out, n)
	}
	return out, nil
}

// LogEntry is one write attempt.
type LogEntry struct {
	Time   time.Time `json:"time"`
	Client string    `json:"client"`
	Key    string    `json:"key"`
	Label  string    `json:"label"`
	Value  float64   `json:"value"`
	Result string    `json:"result"` // "ok", "unverändert", or the reason of the rejection
}

const logSize = 100

// SetWriter enables the write path.
func (e *Engine) SetWriter(w ValueSetter, cfg WriteConfig) {
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	// deep copy: the caller's slice and networks may change later
	allow := make([]*net.IPNet, 0, len(cfg.Allow))
	for _, n := range cfg.Allow {
		if n == nil {
			continue
		}
		allow = append(allow, &net.IPNet{IP: append(net.IP(nil), n.IP...), Mask: append(net.IPMask(nil), n.Mask...)})
	}
	cfg.Allow = allow
	e.mu.Lock()
	e.wsrc, e.wcfg = w, cfg
	e.wgen++
	e.mu.Unlock()
}

// WriteLog returns the last write attempts, newest last.
func (e *Engine) WriteLog() []LogEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]LogEntry(nil), e.wlog...)
}

func (e *Engine) logWrite(l LogEntry) {
	e.wlog = append(e.wlog, l)
	if len(e.wlog) > logSize {
		e.wlog = e.wlog[len(e.wlog)-logSize:]
	}
}

func clientIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	}
	return nil
}

func (e *Engine) allowed(a net.Addr) bool {
	ip := clientIP(a)
	if ip == nil {
		return false
	}
	for _, n := range e.wcfg.Allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// step is one entry of a write request after validation.
type step struct {
	obj   object
	value float64
	same  bool // equals the current CCU value: acknowledged, not forwarded
}

// WriteCoils implements modbus.Backend.
func (e *Engine) WriteCoils(client net.Addr, start int, values []bool) error {
	words := make([]uint16, len(values))
	for i, v := range values {
		if v {
			words[i] = 1
		}
	}
	return e.write(client, table.Coils, start, words)
}

// WriteRegisters implements modbus.Backend.
func (e *Engine) WriteRegisters(client net.Addr, start int, values []uint16) error {
	return e.write(client, table.HoldingRegisters, start, values)
}

// write validates the complete request first; only if every entry passes
// is anything sent to the CCU. Exceptions:
//
//	01 client not on the write allow list
//	02 address not a writable export (orphan, write disabled)
//	03 value invalid (type, range, value list)
//	06 an entry was written less than MinInterval ago
//	04 the CCU did not accept a value (earlier values of a multi-write
//	   may already be set: the CCU has no transactions)
func (e *Engine) write(client net.Addr, area table.Area, start int, words []uint16) error {
	now := time.Now()
	values, h := e.pol.Values()

	e.mu.Lock()
	src := e.wsrc
	cl := ""
	if ip := clientIP(client); ip != nil {
		cl = ip.String()
	}
	reject := func(key, label string, v float64, why string, ex modbus.Exception) error {
		e.logWrite(LogEntry{Time: now, Client: cl, Key: key, Label: label, Value: v, Result: why})
		e.mu.Unlock()
		return ex
	}
	if src == nil {
		return reject("", "", 0, "Schreiben nicht aktiviert", modbus.ExIllegalFunction)
	}
	if !e.allowed(client) {
		return reject("", "", 0, "Client nicht in der Schreibfreigabe", modbus.ExIllegalFunction)
	}
	var steps []step
	for addr := start; addr < start+len(words); {
		i, ok := e.byAddr[addrKey{area, addr}]
		if !ok {
			return reject("", "", 0, fmt.Sprintf("Adresse %d ist kein Wertanfang", addr), modbus.ExIllegalDataAddress)
		}
		o := e.objs[i]
		en := o.entry
		n := max(1, en.Format.Words())
		if addr+n > start+len(words) {
			return reject(en.Key, en.Label, 0, "Wert nur teilweise geschrieben", modbus.ExIllegalDataAddress)
		}
		if en.Orphan || o.regaID == 0 || !en.Writable || !o.meta.writable {
			return reject(en.Key, en.Label, 0, "nicht schreibbar", modbus.ExIllegalDataAddress)
		}
		chunk := words[addr-start : addr-start+n]
		v, err := mapping.Decode(en, e.m.WordOrder, chunk)
		if err != nil {
			return reject(en.Key, en.Label, 0, err.Error(), modbus.ExIllegalDataValue)
		}
		if err := mapping.CheckWriteValue(o.meta.typ, v, o.meta.min, o.meta.max, o.meta.list); err != nil {
			return reject(en.Key, en.Label, v, err.Error(), modbus.ExIllegalDataValue)
		}
		same := false
		if en.Type != rega.Action && quality.Judge(o.regaID, values, h.LastOK, e.MaxAge, now).Valid() {
			if cur, ok := mapping.Encode(en, e.m.WordOrder, values[o.regaID].Value); ok && equalWords(cur, chunk) {
				same = true
			}
		}
		if !same {
			if last, ok := e.lastWrite[en.Key]; ok && now.Sub(last) < e.wcfg.MinInterval {
				return reject(en.Key, en.Label, v, "zu schnell hintereinander", modbus.ExServerDeviceBusy)
			}
		}
		steps = append(steps, step{obj: o, value: v, same: same})
		addr += n
	}
	// Reserve the time slots before releasing the lock, so concurrent
	// clients cannot both pass the rate limit. The previous times are
	// kept to give back the slots of steps that are not executed.
	prev := map[string]time.Time{}
	for _, s := range steps {
		if !s.same {
			if t, ok := e.lastWrite[s.obj.entry.Key]; ok {
				prev[s.obj.entry.Key] = t
			}
			e.lastWrite[s.obj.entry.Key] = now
		}
	}
	timeout := e.wcfg.Timeout
	gen := e.wgen
	e.mu.Unlock()

	// release gives back the reservations of steps[i:] and logs them as
	// not executed.
	release := func(i int, why string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, s := range steps[i:] {
			k := s.obj.entry.Key
			if !s.same {
				if t, ok := prev[k]; ok {
					e.lastWrite[k] = t
				} else {
					delete(e.lastWrite, k)
				}
			}
			e.logWrite(LogEntry{Time: now, Client: cl, Key: k, Label: s.obj.entry.Label, Value: s.value, Result: why})
		}
	}

	for i, s := range steps {
		en := s.obj.entry
		// Exports, write permission or allow list may have changed while
		// earlier steps were executed: then nothing more is sent.
		e.mu.Lock()
		changed := e.wgen != gen
		e.mu.Unlock()
		if changed {
			release(i, "nicht ausgeführt: Freigabe wurde währenddessen geändert")
			return modbus.ExServerDeviceFail
		}
		if s.same {
			e.record(LogEntry{Time: now, Client: cl, Key: en.Key, Label: en.Label, Value: s.value, Result: "unverändert"})
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := src.SetValue(ctx, s.obj.regaID, s.obj.meta.typ, s.value)
		cancel()
		if err != nil {
			why := "CCU hat abgelehnt"
			if !errors.Is(err, rega.ErrRejected) {
				why = "CCU-Fehler: " + err.Error()
			}
			e.record(LogEntry{Time: now, Client: cl, Key: en.Key, Label: en.Label, Value: s.value, Result: why})
			if i+1 < len(steps) {
				release(i+1, "nicht ausgeführt: vorheriger Wert abgelehnt")
			}
			return modbus.ExServerDeviceFail
		}
		e.record(LogEntry{Time: now, Client: cl, Key: en.Key, Label: en.Label, Value: s.value, Result: "ok (von CCU angenommen, nicht bestätigt ausgeführt)"})
	}
	return nil
}

func (e *Engine) record(l LogEntry) {
	e.mu.Lock()
	e.logWrite(l)
	e.mu.Unlock()
}

func equalWords(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FormatAllow renders networks for display.
func FormatAllow(n []*net.IPNet) []string {
	out := make([]string, len(n))
	for i, x := range n {
		ones, bits := x.Mask.Size()
		if ones == bits {
			out[i] = x.IP.String()
		} else {
			out[i] = x.IP.String() + "/" + strconv.Itoa(ones)
		}
	}
	return out
}

// The engine is the write backend of the Modbus server.
var _ modbus.Backend = (*Engine)(nil)
