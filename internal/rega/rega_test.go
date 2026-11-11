// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/rega/rega_test.go
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

package rega

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Output captured from the test CCU (OpenCCU 3.89.11) on 2026-11-11,
// shortened, plus synthetic room/function/value-list lines.
const sampleOutput = `D|1099|CCU%2DModbusClient|MBC075905DE02|Modbus%20MBC075905DE02|Modbus
C|1115|MBC075905DE02%3A1|Modbus%20MBC075905DE02%3A1|DIGITAL%5FINPUT
P|1117|CCU%2DModbusClient%2EMBC075905DE02%3A1%2ESTATE|2|0|false|true|||5|true|1794358831
C|1127|MBC075905DE02%3A5|B%FCro%20Licht|SWITCH
P|1129|CCU%2DModbusClient%2EMBC075905DE02%3A5%2ESTATE|2|1|false|true|||7|false|1794232809
P|1140|CCU%2DModbusClient%2EMBC075905DE02%3A5%2EPRESS%5FSHORT|2|0|false|true|||2||0
P|1141|CCU%2DModbusClient%2EMBC075905DE02%3A5%2ELEVEL|4|0|0%2E000000|1%2E000000|100%25||7|0%2E500000|1794232809
P|1142|CCU%2DModbusClient%2EMBC075905DE02%3A5%2EMODE|16|29|0|2||AUTO%3BMANU%3BPARTY|7|1|1794232809
P|1143|CCU%2DModbusClient%2EMBC075905DE02%3A5%2ETEXT|20|11|||||7|abc|1794232809
R|1127|K%C3%BCche
F|1127|Licht
F|1115|Sicherheit
V|40|Alarmmeldungen|16|0|0|65000|||7|0|1794168447|true
V|950|Anwesenheit|2|2|||||7|true|1794168447|false
V|1034|DutyCycle|4|0|%2D1|100|%25||7|%2D1%2E000000|1794358800|false
V|1200|Modus|16|29|||%20|Aus%3BEin%3BAuto|7|2|1794358800|false
V|1201|Text|20|11|||||7|hallo|1794358800|false`

func TestParseCatalog(t *testing.T) {
	cat, err := parseCatalog(strings.Split(sampleOutput, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Devices) != 1 || len(cat.Devices[0].Channels) != 2 {
		t.Fatalf("structure: %+v", cat)
	}
	d := cat.Devices[0]
	if d.Interface != "CCU-ModbusClient" || d.Address != "MBC075905DE02" || d.Name != "Modbus MBC075905DE02" {
		t.Errorf("device: %+v", d)
	}
	c := d.Channels[1]
	if c.Name != "Büro Licht" {
		t.Errorf("latin-1 name: %q", c.Name)
	}
	if len(c.Rooms) != 1 || c.Rooms[0] != "Küche" || len(c.Functions) != 1 || c.Functions[0] != "Licht" {
		t.Errorf("rooms/functions: %v %v", c.Rooms, c.Functions)
	}
	if f := d.Channels[0].Functions; len(f) != 1 || f[0] != "Sicherheit" {
		t.Errorf("channel 1 functions: %v", f)
	}
	want := []struct {
		id       string
		typ      Type
		r, w, ev bool
	}{
		{"CCU-ModbusClient.MBC075905DE02:5.STATE", Bool, true, true, true},
		{"CCU-ModbusClient.MBC075905DE02:5.PRESS_SHORT", Action, false, true, false},
		{"CCU-ModbusClient.MBC075905DE02:5.LEVEL", Float, true, true, true},
		{"CCU-ModbusClient.MBC075905DE02:5.MODE", Enum, true, true, true},
		{"CCU-ModbusClient.MBC075905DE02:5.TEXT", String, true, true, true},
	}
	for i, w := range want {
		p := c.Points[i]
		if p.ID != w.id || p.Type != w.typ || p.Readable != w.r || p.Writable != w.w || p.Events != w.ev {
			t.Errorf("point %d: %+v, want %+v", i, p, w)
		}
	}
	lv := c.Points[2]
	if lv.Param != "LEVEL" || lv.Min == nil || *lv.Min != 0 || lv.Max == nil || *lv.Max != 1 || lv.Unit != "100%" || lv.Value != "0.500000" {
		t.Errorf("level: %+v", lv)
	}
	if c.Points[0].Min != nil {
		t.Errorf("bool min must be nil")
	}
	if m := c.Points[3].ValueList; len(m) != 3 || m[2] != "PARTY" {
		t.Errorf("value list: %v", m)
	}
	if !c.Points[1].Type.Offered() || c.Points[4].Type.Offered() {
		t.Error("offered: action yes, string no")
	}

	if len(cat.SysVars) != 5 {
		t.Fatalf("sysvars: %d", len(cat.SysVars))
	}
	sv := map[string]SysVar{}
	for _, v := range cat.SysVars {
		sv[v.Name] = v
	}
	if v := sv["Alarmmeldungen"]; v.Type != Integer || !v.Internal || v.ID != "SV:40" {
		t.Errorf("Alarmmeldungen: %+v", v)
	}
	if v := sv["Anwesenheit"]; v.Type != Bool || v.Internal || !v.Writable {
		t.Errorf("Anwesenheit: %+v", v)
	}
	if v := sv["DutyCycle"]; v.Type != Float || v.Min == nil || *v.Min != -1 || v.Unit != "%" {
		t.Errorf("DutyCycle: %+v", v)
	}
	if v := sv["Modus"]; v.Type != Enum || len(v.ValueList) != 3 || v.Unit != " " {
		t.Errorf("Modus: %+v", v)
	}
	if v := sv["Text"]; v.Type != String {
		t.Errorf("Text: %+v", v)
	}
}

func TestParseCatalogRejectsMalformed(t *testing.T) {
	for _, l := range []string{
		"P|1|x|2|0|||||7||0", // point without channel
		"D|x|a|b|c|d",
		"X|1",
		"V|1|n|2",
		"C|1|a|b|c", // channel without device
	} {
		if _, err := parseCatalog([]string{l}); err == nil {
			t.Errorf("accepted %q", l)
		}
	}
}

func TestSplitOutput(t *testing.T) {
	lines, err := splitOutput([]byte("a\r\nb\r\n#END#\r\n<xml><exec>/rega.exe</exec></xml>"))
	if err != nil || len(lines) != 2 || lines[1] != "b" {
		t.Fatalf("%q %v", lines, err)
	}
	if _, err := splitOutput([]byte("a\r\nb\r\n<xml></xml>")); !errors.Is(err, ErrAborted) {
		t.Fatalf("missing marker: %v", err)
	}
}

func TestLiteral(t *testing.T) {
	ok := []struct {
		t    Type
		v    float64
		want string
	}{
		{Bool, 1, "true"}, {Bool, 0, "false"}, {Action, 1, "true"},
		{Integer, -5, "-5"}, {Enum, 2, "2"}, {Float, 21.5, "21.5"}, {Float, -0.25, "-0.25"},
	}
	for _, c := range ok {
		got, err := Literal(c.t, c.v)
		if err != nil || got != c.want {
			t.Errorf("%s %v -> %q %v", c.t, c.v, got, err)
		}
	}
	for _, c := range []struct {
		t Type
		v float64
	}{{Integer, 1.5}, {Enum, 0.1}, {String, 1}, {Other, 1}, {Float, nanValue()}} {
		if _, err := Literal(c.t, c.v); err == nil {
			t.Errorf("%s %v accepted", c.t, c.v)
		}
	}
}

func nanValue() float64 { return math.NaN() }

func fakeReGa(t *testing.T, reply func(script string) string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, reply(string(b))+"\r\n<xml><exec>/rega.exe</exec></xml>")
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func TestReadValues(t *testing.T) {
	var got string
	c := fakeReGa(t, func(s string) string {
		got = s
		return "1117|true|1794358831\r\n1034|%2D1%2E000000|1794358800\r\n99|-|0\r\n#END#"
	})
	r, err := c.ReadValues(context.Background(), []int{1117, 1034, 99, 1117})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\"1117\t1034\t99\"") {
		t.Errorf("script ids (deduplicated): %q", got)
	}
	if v := r[1117]; !v.Found || v.Value != "true" || v.Timestamp != 1794358831 {
		t.Errorf("1117: %+v", v)
	}
	if v := r[1034]; v.Value != "-1.000000" {
		t.Errorf("1034: %+v", v)
	}
	if v := r[99]; v.Found {
		t.Errorf("99 found")
	}
}

func TestReadValuesRejectsIncompleteOrForeign(t *testing.T) {
	bad := map[string]string{
		"missing id":      "1|a|5\r\n#END#",
		"duplicate id":    "1|a|5\r\n1|a|5\r\n2|b|5\r\n#END#",
		"foreign id":      "1|a|5\r\n2|b|5\r\n3|c|5\r\n#END#",
		"bad timestamp":   "1|a|x\r\n2|b|5\r\n#END#",
		"negative ts":     "1|a|-5\r\n2|b|5\r\n#END#",
		"missing with ts": "1|-|7\r\n2|b|5\r\n#END#",
		"no marker":       "1|a|5\r\n2|b|5",
		"extra field":     "1|a|5|x\r\n2|b|5\r\n#END#",
	}
	for name, reply := range bad {
		c := fakeReGa(t, func(string) string { return reply })
		if _, err := c.ReadValues(context.Background(), []int{1, 2}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := fakeReGa(t, func(string) string { return "#END#" })
	if _, err := c.ReadValues(context.Background(), []int{0}); err == nil {
		t.Error("id 0 accepted")
	}
}

func TestResponseLimit(t *testing.T) {
	c := fakeReGa(t, func(string) string { return strings.Repeat("x", 200) + "\r\n#END#" })
	c.MaxResponse = 100
	if _, err := c.Run(context.Background(), "x"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestSetValue(t *testing.T) {
	var got string
	c := fakeReGa(t, func(s string) string { got = s; return "OK\r\n#END#" })
	if err := c.SetValue(context.Background(), 1129, Bool, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "dom.GetObject(1129)") || !strings.Contains(got, "o.State(true)") {
		t.Errorf("script: %q", got)
	}
	if err := c.SetValue(context.Background(), 7, Float, 21.5); err != nil || !strings.Contains(got, "o.State(21.5)") {
		t.Errorf("float: %v %q", err, got)
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := c.SetValue(context.Background(), 7, Float, v); err == nil {
			t.Errorf("%v accepted", v)
		}
	}
	if err := c.SetValue(context.Background(), 0, Bool, 1); err == nil {
		t.Error("id 0 accepted")
	}
	if err := c.SetValue(context.Background(), 7, String, 1); err == nil {
		t.Error("string accepted")
	}
	for _, reply := range []string{"REJECTED\r\n#END#", "MISSING\r\n#END#", "OK\r\nOK\r\n#END#"} {
		c2 := fakeReGa(t, func(string) string { return reply })
		if err := c2.SetValue(context.Background(), 5, Integer, 1); !errors.Is(err, ErrRejected) {
			t.Errorf("%q: %v", reply, err)
		}
	}
	c3 := fakeReGa(t, func(string) string { return "" })
	if err := c3.SetValue(context.Background(), 5, Integer, 1); !errors.Is(err, ErrAborted) {
		t.Errorf("aborted: %v", err)
	}
}

func TestCatalogCloneIsDeep(t *testing.T) {
	cat, err := parseCatalog(strings.Split(sampleOutput, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := cat.Clone()
	*c.Devices[0].Channels[1].Points[2].Max = 99
	c.Devices[0].Channels[1].Points[3].ValueList[0] = "X"
	c.SysVars[0].Name = "Y"
	c.Devices[0].Channels[1].Rooms[0] = "Z"
	p := cat.Devices[0].Channels[1].Points
	if *p[2].Max != 1 || p[3].ValueList[0] != "AUTO" || cat.SysVars[0].Name == "Y" || cat.Devices[0].Channels[1].Rooms[0] == "Z" {
		t.Fatal("clone shares state")
	}
	var nilCat *Catalog
	if nilCat.Clone() != nil {
		t.Fatal("nil clone")
	}
}
