// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/table/loopback.go
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

package table

// Loopback is a write backend that stores written values directly in the
// table. It stands in for the CCU until the CCU connection exists
// (stage 2) and is used by the test image (-demo) and the tests.
type Loopback struct {
	T *Table
}

// WriteCoils stores coil values.
func (l Loopback) WriteCoils(start int, values []bool) error {
	l.T.Update(func(w Writer) {
		for i, v := range values {
			var x uint16
			if v {
				x = 1
			}
			w.Set(Coils, start+i, x)
		}
	})
	return nil
}

// WriteRegisters stores holding register values.
func (l Loopback) WriteRegisters(start int, values []uint16) error {
	l.T.Update(func(w Writer) {
		w.Set(HoldingRegisters, start, values...)
	})
	return nil
}
