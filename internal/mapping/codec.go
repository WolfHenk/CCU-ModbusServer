// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/mapping/codec.go
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

package mapping

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

// DefaultFormat is the suggested format for a CCU type.
func DefaultFormat(t rega.Type) (Format, bool) {
	switch t {
	case rega.Bool, rega.Action:
		return Bit, true
	case rega.Float:
		return Float32, true
	case rega.Integer:
		return Int32, true
	case rega.Enum:
		return Uint16, true
	}
	return "", false
}

// formatRange is the numeric range of a register format.
func formatRange(f Format) (float64, float64) {
	switch f {
	case Int16:
		return math.MinInt16, math.MaxInt16
	case Uint16:
		return 0, math.MaxUint16
	case Int32:
		return math.MinInt32, math.MaxInt32
	case Uint32:
		return 0, math.MaxUint32
	case Float32:
		return -math.MaxFloat32, math.MaxFloat32
	}
	return 0, 1
}

// CheckFormat checks that a format and scale fit the CCU type and range.
// min/max may be nil (unknown); then only the type is checked. For value
// lists the index range 0..len-1 is used.
func CheckFormat(t rega.Type, f Format, scale float64, min, max *float64, listLen int) error {
	if !t.Offered() {
		return fmt.Errorf("Typ %s wird nicht angeboten", t)
	}
	bitType := t == rega.Bool || t == rega.Action
	if bitType != (f == Bit) {
		return fmt.Errorf("Format %s passt nicht zu Typ %s", f, t)
	}
	if f == Bit {
		return nil
	}
	if !f.Valid() {
		return fmt.Errorf("unbekanntes Format %q", f)
	}
	if f == Float32 {
		if scale != 1 {
			return fmt.Errorf("Skalierung nur für Ganzzahlformate")
		}
		return nil
	}
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		return fmt.Errorf("ungültige Skalierung %v", scale)
	}
	lo, hi := formatRange(f)
	if t == rega.Enum && listLen > 0 {
		z, n := 0.0, float64(listLen-1)
		min, max = &z, &n
	}
	if min != nil && *min*scale < lo {
		return fmt.Errorf("Minimum %v × %v passt nicht in %s", *min, scale, f)
	}
	if max != nil && *max*scale > hi {
		return fmt.Errorf("Maximum %v × %v passt nicht in %s", *max, scale, f)
	}
	return nil
}

// parseCCU converts a ReGa value string to a number.
func parseCCU(t rega.Type, s string) (float64, bool) {
	s = strings.TrimSpace(s)
	switch t {
	case rega.Bool:
		switch s {
		case "true":
			return 1, true
		case "false":
			return 0, true
		}
		return 0, false
	case rega.Action:
		return 0, true // actions always read 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// order puts two 16-bit words into the configured word order.
func order(hi, lo uint16, wordOrder string) []uint16 {
	if wordOrder == "CDAB" {
		return []uint16{lo, hi}
	}
	return []uint16{hi, lo}
}

func unorder(w []uint16, wordOrder string) uint32 {
	if wordOrder == "CDAB" {
		return uint32(w[1])<<16 | uint32(w[0])
	}
	return uint32(w[0])<<16 | uint32(w[1])
}

// Encode converts a ReGa value to Modbus words (bits as 0/1 in one word).
// ok is false if the value cannot be read or represented; the caller then
// keeps the substitute value (float32: NaN).
func Encode(e Entry, wordOrder string, value string) (words []uint16, ok bool) {
	v, ok := parseCCU(e.Type, value)
	if e.Format == Float32 {
		f := float32(v)
		if !ok {
			f = float32(math.NaN())
		}
		b := math.Float32bits(f)
		return order(uint16(b>>16), uint16(b), wordOrder), ok
	}
	if e.Format == Bit {
		if !ok {
			return []uint16{0}, false
		}
		if v != 0 {
			return []uint16{1}, true
		}
		return []uint16{0}, true
	}
	if !ok {
		return nil, false
	}
	x := math.Round(v * e.Scale)
	lo, hi := formatRange(e.Format)
	if x < lo || x > hi {
		return nil, false
	}
	switch e.Format {
	case Int16:
		return []uint16{uint16(int16(x))}, true
	case Uint16:
		return []uint16{uint16(x)}, true
	case Int32:
		u := uint32(int32(x))
		return order(uint16(u>>16), uint16(u), wordOrder), true
	case Uint32:
		u := uint32(x)
		return order(uint16(u>>16), uint16(u), wordOrder), true
	}
	return nil, false
}

// NaNWords returns the float32 NaN substitute in the given word order.
func NaNWords(wordOrder string) []uint16 {
	b := math.Float32bits(float32(math.NaN()))
	return order(uint16(b>>16), uint16(b), wordOrder)
}

// Decode converts written Modbus words back to a CCU value (scale
// removed). Bits: words[0] != 0.
func Decode(e Entry, wordOrder string, words []uint16) (float64, error) {
	if len(words) != max(1, e.Format.Words()) {
		return 0, fmt.Errorf("expected %d words, got %d", e.Format.Words(), len(words))
	}
	var v float64
	switch e.Format {
	case Bit:
		if words[0] != 0 {
			return 1, nil
		}
		return 0, nil
	case Int16:
		v = float64(int16(words[0]))
	case Uint16:
		v = float64(words[0])
	case Int32:
		v = float64(int32(unorder(words, wordOrder)))
	case Uint32:
		v = float64(unorder(words, wordOrder))
	case Float32:
		f := math.Float32frombits(unorder(words, wordOrder))
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return 0, fmt.Errorf("not a finite number")
		}
		return float64(f), nil
	default:
		return 0, fmt.Errorf("unknown format %q", e.Format)
	}
	return v / e.Scale, nil
}
