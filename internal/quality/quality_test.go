// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/quality/quality_test.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.2.1
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

package quality

import (
	"testing"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

func TestJudge(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Second)
	maxAge := 10 * time.Second
	vals := map[int]rega.Reading{
		1:  {Found: true, Value: "21.5", Timestamp: 100},
		2:  {Found: true, Value: "0", Timestamp: 0},     // never set
		3:  {},                                          // gone from ReGa
		4:  {Found: true, Value: "true", Timestamp: 50}, // device A unreachable
		5:  {Found: true, Value: "false", Timestamp: 1}, // device B reachable
		6:  {Found: true, Value: "1", Timestamp: 1000000},
		7:  {Found: true, Value: "1", Timestamp: 0}, // never set on unreachable device
		99: {Found: false},
	}
	cases := []struct {
		name      string
		id, un    int
		lastOK    time.Time
		want      Reason
		wantValid bool
	}{
		{"good", 1, 0, fresh, Good, true},
		{"old device timestamp is still good", 6, 0, fresh, Good, true},
		{"never set", 2, 0, fresh, NeverSet, false},
		{"missing", 3, 0, fresh, Missing, false},
		{"unreach", 1, 4, fresh, Unreach, false},
		{"reachable device", 1, 5, fresh, Good, true},
		{"unreach beats never set", 7, 4, fresh, Unreach, false},
		{"unreach datapoint missing -> ignore", 1, 99, fresh, Good, true},
		{"not polled", 42, 0, fresh, Unknown, false},
		{"source down", 1, 0, now.Add(-time.Minute), SourceDown, false},
		{"never polled", 1, 0, time.Time{}, SourceDown, false},
	}
	for _, c := range cases {
		got := Judge(c.id, c.un, vals, c.lastOK, maxAge, now)
		if got != c.want || got.Valid() != c.wantValid {
			t.Errorf("%s: got %v (%s)", c.name, got, got)
		}
	}
}

func TestUnreachIDs(t *testing.T) {
	cat := &rega.Catalog{Devices: []rega.Device{
		{Address: "A", Channels: []rega.Channel{
			{Address: "A:0", Points: []rega.Point{{ReGaID: 10, Param: "UNREACH"}, {ReGaID: 11, Param: "LOWBAT"}}},
			{Address: "A:1", Points: []rega.Point{{ReGaID: 12, Param: "STATE"}}},
		}},
		{Address: "B", Channels: []rega.Channel{
			{Address: "B:1", Points: []rega.Point{{ReGaID: 20, Param: "STATE"}}},
		}},
		{Address: "C", Channels: []rega.Channel{
			{Address: "C:10", Points: []rega.Point{{ReGaID: 30, Param: "UNREACH"}}}, // not channel 0
			{Address: "C:1", Points: []rega.Point{{ReGaID: 31, Param: "STATE"}}},
		}},
	}}
	m := UnreachIDs(cat)
	if m[12] != 10 || m[11] != 10 || m[10] != 10 {
		t.Errorf("device A: %v", m)
	}
	if _, ok := m[20]; ok {
		t.Error("device B without UNREACH mapped")
	}
	if _, ok := m[31]; ok {
		t.Error("UNREACH outside channel 0 used")
	}
	if len(UnreachIDs(nil)) != 0 {
		t.Error("nil catalog")
	}
	for r := Good; r <= Unknown; r++ {
		if r.String() == "unbekannt" {
			t.Errorf("reason %d without text", r)
		}
	}
}
