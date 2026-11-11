// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/mapping/codec_test.go
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
	"math"
	"strconv"
	"testing"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

func eq(a, b []uint16) bool {
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

func TestEncode(t *testing.T) {
	cases := []struct {
		name  string
		e     Entry
		order string
		in    string
		want  []uint16
		ok    bool
	}{
		{"float 21.5 ABCD", Entry{Type: rega.Float, Format: Float32}, "ABCD", "21.500000", []uint16{0x41AC, 0x0000}, true},
		{"float 21.5 CDAB", Entry{Type: rega.Float, Format: Float32}, "CDAB", "21.500000", []uint16{0x0000, 0x41AC}, true},
		{"float garbage -> NaN", Entry{Type: rega.Float, Format: Float32}, "ABCD", "", []uint16{0x7FC0, 0x0000}, false},
		{"bool true", Entry{Type: rega.Bool, Format: Bit}, "ABCD", "true", []uint16{1}, true},
		{"bool false", Entry{Type: rega.Bool, Format: Bit}, "ABCD", "false", []uint16{0}, true},
		{"bool garbage", Entry{Type: rega.Bool, Format: Bit}, "ABCD", "1", []uint16{0}, false},
		{"action reads 0", Entry{Type: rega.Action, Format: Bit}, "ABCD", "true", []uint16{0}, true},
		{"enum index", Entry{Type: rega.Enum, Format: Uint16, Scale: 1}, "ABCD", "2", []uint16{2}, true},
		{"int32 negative", Entry{Type: rega.Integer, Format: Int32, Scale: 1}, "ABCD", "-2", []uint16{0xFFFF, 0xFFFE}, true},
		{"int32 CDAB", Entry{Type: rega.Integer, Format: Int32, Scale: 1}, "CDAB", "65536", []uint16{0x0000, 0x0001}, true},
		{"int16 x10", Entry{Type: rega.Float, Format: Int16, Scale: 10}, "ABCD", "-12.34", []uint16{uint16(0xFF85)}, true}, // -123
		{"uint16 overflow", Entry{Type: rega.Float, Format: Uint16, Scale: 100}, "ABCD", "700", nil, false},
		{"uint16 negative", Entry{Type: rega.Integer, Format: Uint16, Scale: 1}, "ABCD", "-1", nil, false},
		{"uint32", Entry{Type: rega.Integer, Format: Uint32, Scale: 1}, "ABCD", "70000", []uint16{0x0001, 0x1170}, true},
	}
	for _, c := range cases {
		got, ok := Encode(c.e, c.order, c.in)
		if ok != c.ok || (c.want != nil && !eq(got, c.want)) {
			t.Errorf("%s: got %04x ok=%v", c.name, got, ok)
		}
	}
	if w := NaNWords("CDAB"); !eq(w, []uint16{0, 0x7FC0}) {
		t.Errorf("NaN CDAB %04x", w)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	for _, c := range []struct {
		e     Entry
		order string
		v     float64
	}{
		{Entry{Type: rega.Float, Format: Float32}, "ABCD", 21.5},
		{Entry{Type: rega.Float, Format: Float32}, "CDAB", -3.25},
		{Entry{Type: rega.Integer, Format: Int32, Scale: 1}, "CDAB", -70000},
		{Entry{Type: rega.Integer, Format: Uint32, Scale: 1}, "ABCD", 4000000000},
		{Entry{Type: rega.Float, Format: Int16, Scale: 10}, "ABCD", -12.3},
		{Entry{Type: rega.Enum, Format: Uint16, Scale: 1}, "ABCD", 3},
	} {
		w, ok := Encode(c.e, c.order, formatFloat(c.v))
		if !ok {
			t.Fatalf("encode %v", c)
		}
		got, err := Decode(c.e, c.order, w)
		if err != nil || math.Abs(got-c.v) > 1e-6 {
			t.Errorf("%v: got %v %v", c, got, err)
		}
	}
	if v, _ := Decode(Entry{Format: Bit}, "ABCD", []uint16{1}); v != 1 {
		t.Error("bit")
	}
	if _, err := Decode(Entry{Type: rega.Float, Format: Float32}, "ABCD", NaNWords("ABCD")); err == nil {
		t.Error("NaN written accepted")
	}
	inf := math.Float32bits(float32(math.Inf(1)))
	if _, err := Decode(Entry{Type: rega.Float, Format: Float32}, "ABCD", []uint16{uint16(inf >> 16), uint16(inf)}); err == nil {
		t.Error("Inf written accepted")
	}
	if _, err := Decode(Entry{Format: Int32, Scale: 1}, "ABCD", []uint16{1}); err == nil {
		t.Error("half value accepted")
	}
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func TestCheckFormat(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	ok := []struct {
		t     rega.Type
		f     Format
		scale float64
		min   *float64
		max   *float64
		list  int
	}{
		{rega.Float, Float32, 1, f(-3276.8), f(3276.7), 0},
		{rega.Float, Int16, 10, f(-3276.8), f(3276.7), 0},
		{rega.Bool, Bit, 0, nil, nil, 0},
		{rega.Action, Bit, 0, nil, nil, 0},
		{rega.Enum, Uint16, 1, nil, nil, 8},
		{rega.Integer, Int32, 1, f(0), f(255), 0},
		{rega.Integer, Float32, 1, f(0), f(1000), 0},
	}
	for _, c := range ok {
		if err := CheckFormat(c.t, c.f, c.scale, c.min, c.max, c.list); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	bad := []struct {
		t     rega.Type
		f     Format
		scale float64
		min   *float64
		max   *float64
	}{
		{rega.Float, Int16, 100, f(-3276.8), f(3276.7)}, // overflow
		{rega.Float, Uint16, 1, f(-1), f(100)},          // negative
		{rega.Bool, Uint16, 1, nil, nil},
		{rega.Float, Bit, 0, nil, nil},
		{rega.String, Uint16, 1, nil, nil},
		{rega.Other, Uint16, 1, nil, nil},
		{rega.Float, Float32, 10, nil, nil},
		{rega.Integer, Int32, 0, nil, nil},
		{rega.Integer, Int32, math.NaN(), nil, nil},
		// Regression M3: conservative metadata handling
		{rega.Integer, Uint16, 1, nil, nil},           // unknown range
		{rega.Float, Int16, 10, f(math.NaN()), f(10)}, // NaN min
		{rega.Float, Int32, 1, f(0), f(math.Inf(1))},  // Inf max
		{rega.Float, Float32, 1, f(5), f(1)},          // min > max
		{rega.Enum, Uint16, 1, nil, nil},              // no value list
		{rega.Integer, Float32, 1, f(0), f(1 << 30)},  // float32 not exact
		{rega.Integer, Float32, 1, nil, nil},          // float32, unknown range
		{rega.Float, Int16, 2e6, f(0), f(1)},          // absurd scale
	}
	for _, c := range bad {
		if err := CheckFormat(c.t, c.f, c.scale, c.min, c.max, 0); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
	for _, typ := range []rega.Type{rega.Bool, rega.Action, rega.Float, rega.Integer, rega.Enum} {
		if _, ok := DefaultFormat(typ); !ok {
			t.Errorf("no default for %s", typ)
		}
	}
	if _, ok := DefaultFormat(rega.String); ok {
		t.Error("default for string")
	}
}

// Regression M1: a finite value beyond float32 must not become +Inf.
func TestEncodeFloat32Overflow(t *testing.T) {
	e := Entry{Type: rega.Float, Format: Float32}
	for _, in := range []string{"1e100", "-1e100", "1e39"} {
		w, ok := Encode(e, "ABCD", in)
		if ok || !eq(w, NaNWords("ABCD")) {
			t.Errorf("%s: %04x ok=%v", in, w, ok)
		}
	}
	if _, ok := Encode(e, "ABCD", "3.4e38"); !ok {
		t.Error("max float32 rejected")
	}
}

// Regression M2: central write validation.
func TestCheckWriteValue(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	ok := []struct {
		t        rega.Type
		v        float64
		min, max *float64
		list     int
	}{
		{rega.Bool, 1, nil, nil, 0}, {rega.Bool, 0, nil, nil, 0}, {rega.Action, 1, nil, nil, 0},
		{rega.Enum, 0, nil, nil, 3}, {rega.Enum, 2, nil, nil, 3},
		{rega.Integer, 5, f(0), f(10), 0}, {rega.Float, 21.5, f(4.5), f(30.5), 0},
		{rega.Float, 1e9, nil, nil, 0},
	}
	for _, c := range ok {
		if err := CheckWriteValue(c.t, c.v, c.min, c.max, c.list); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []struct {
		t        rega.Type
		v        float64
		min, max *float64
		list     int
	}{
		{rega.Bool, 2, nil, nil, 0}, {rega.Action, -1, nil, nil, 0},
		{rega.Enum, 3, nil, nil, 3}, {rega.Enum, -1, nil, nil, 3}, {rega.Enum, 1.5, nil, nil, 3}, {rega.Enum, 0, nil, nil, 0},
		{rega.Integer, 5.5, f(0), f(10), 0}, {rega.Integer, 11, f(0), f(10), 0}, {rega.Integer, -1, f(0), f(10), 0},
		{rega.Float, 30.6, f(4.5), f(30.5), 0}, {rega.Float, 4.4, f(4.5), f(30.5), 0},
		{rega.Float, math.NaN(), nil, nil, 0}, {rega.Float, math.Inf(1), nil, nil, 0},
		{rega.Float, 1, f(math.NaN()), nil, 0},
		{rega.String, 1, nil, nil, 0}, {rega.Other, 1, nil, nil, 0},
	}
	for _, c := range bad {
		if err := CheckWriteValue(c.t, c.v, c.min, c.max, c.list); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}
