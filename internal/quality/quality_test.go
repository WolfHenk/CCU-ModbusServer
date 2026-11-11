// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/quality/quality_test.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.2.3
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
		1: {Found: true, Value: "21.5", Timestamp: 100},
		2: {Found: true, Value: "0", Timestamp: 0},
		3: {},
		6: {Found: true, Value: "1", Timestamp: 1000000},
	}
	cases := []struct {
		name   string
		id     int
		lastOK time.Time
		want   Reason
	}{
		{"good", 1, fresh, Good},
		{"old device timestamp is still good", 6, fresh, Good},
		{"never set", 2, fresh, NeverSet},
		{"missing", 3, fresh, Missing},
		{"not polled", 42, fresh, Unknown},
		{"source down", 1, now.Add(-time.Minute), SourceDown},
		{"never polled", 1, time.Time{}, SourceDown},
	}
	for _, c := range cases {
		if got := Judge(c.id, vals, c.lastOK, maxAge, now); got != c.want || got.Valid() != (c.want == Good) {
			t.Errorf("%s: got %v (%s)", c.name, got, got)
		}
	}
}

// Regression (Wolfram, Bad-oben-Ventil): UNREACH = true while a measured
// value keeps arriving. The value stays valid; the warning is separate.
func TestUnreachDoesNotInvalidateReceivedValue(t *testing.T) {
	now := time.Now()
	vals := map[int]rega.Reading{
		100: {Found: true, Value: "true", Timestamp: 50},              // UNREACH of the valve
		101: {Found: true, Value: "21.0", Timestamp: now.Unix() - 30}, // ACTUAL_TEMPERATURE, just received
		102: {Found: true, Value: "false", Timestamp: 1},              // UNREACH of another device
		103: {Found: true, Value: "", Timestamp: 1},                   // UNREACH without clear false
		104: {Found: false},                                           // UNREACH gone
	}
	if r := Judge(101, vals, now, 10*time.Second, now); r != Good {
		t.Fatalf("value invalidated by UNREACH: %v", r)
	}
	cases := []struct {
		un   int
		want Comm
		warn bool
	}{
		{100, CommUnreach, true}, {102, CommOK, false}, {103, CommUnreach, true},
		{104, CommUnknown, true}, {999, CommUnknown, true}, {0, CommNone, false},
	}
	for _, c := range cases {
		got := CommState(c.un, vals)
		if got != c.want || got.Warning() != c.warn {
			t.Errorf("unreach id %d: %v", c.un, got)
		}
	}
}

func TestUnreachIDsAndPollIDs(t *testing.T) {
	cat := &rega.Catalog{Devices: []rega.Device{
		{Address: "A", Channels: []rega.Channel{
			{Address: "A:0", Points: []rega.Point{{ReGaID: 10, Param: "UNREACH"}, {ReGaID: 11, Param: "LOWBAT"}}},
			{Address: "A:1", Points: []rega.Point{{ReGaID: 12, Param: "STATE"}}},
		}},
		{Address: "B", Channels: []rega.Channel{{Address: "B:1", Points: []rega.Point{{ReGaID: 20, Param: "STATE"}}}}},
		{Address: "C", Channels: []rega.Channel{
			{Address: "C:10", Points: []rega.Point{{ReGaID: 30, Param: "UNREACH"}}},
			{Address: "C:1", Points: []rega.Point{{ReGaID: 31, Param: "STATE"}}},
		}},
	}}
	m := UnreachIDs(cat)
	if m[12] != 10 || m[11] != 10 {
		t.Errorf("device A: %v", m)
	}
	if _, ok := m[20]; ok {
		t.Error("device B mapped")
	}
	if _, ok := m[31]; ok {
		t.Error("UNREACH outside channel 0 used")
	}
	got := PollIDs([]int{12, 31, 12, 0, 11}, m)
	want := []int{12, 10, 31, 11}
	if len(got) != len(want) {
		t.Fatalf("poll ids %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("poll ids %v want %v", got, want)
		}
	}
}

func TestReception(t *testing.T) {
	r := NewReception()
	base := int64(1_000_000)
	// periodic sensor: an update about every 180 s (with jitter)
	jitter := []int64{0, 5, -7, 3, 0, -4, 6, 2, -1, 0}
	ts := base
	for i := 0; i < len(jitter); i++ {
		ts += 180 + jitter[i]
		r.Observe(map[int]rega.Reading{1: {Found: true, Timestamp: ts}})
		// polling the unchanged value again is not a new update
		r.Observe(map[int]rega.Reading{1: {Found: true, Timestamp: ts}})
	}
	// the first observation only sets the start point
	info := r.Info(1, time.Unix(ts+60, 0))
	if info.Samples != len(jitter)-1 || info.Periodic != TriYes || info.Typical < 170 || info.Typical > 190 || info.Overdue != TriNo || info.LastUpdate != ts {
		t.Fatalf("periodic: %+v", info)
	}
	if info := r.Info(1, time.Unix(ts+3*190+1, 0)); info.Overdue != TriYes {
		t.Fatalf("periodic silent: %+v", info)
	}

	// Regression (ChatGPT review 0.2.3, B): an event-driven contact with
	// random actuations must never become "overdue", even with many
	// observations.
	random := []int64{40, 3600, 7, 86400, 300, 12, 5000, 90000, 60, 2}
	ts = base
	r.Observe(map[int]rega.Reading{2: {Found: true, Timestamp: ts}})
	for _, d := range random {
		ts += d
		r.Observe(map[int]rega.Reading{2: {Found: true, Timestamp: ts}})
	}
	info = r.Info(2, time.Unix(ts+30*86400, 0))
	if info.Periodic != TriNo || info.Overdue != TriUnknown || info.Typical != 0 {
		t.Fatalf("event driven: %+v", info)
	}
	// only four regular intervals are not enough
	ts = base
	for i := 0; i < 5; i++ {
		r.Observe(map[int]rega.Reading{3: {Found: true, Timestamp: ts}})
		ts += 100
	}
	if info := r.Info(3, time.Unix(ts+100000, 0)); info.Overdue != TriUnknown || info.Periodic != TriUnknown {
		t.Fatalf("too few samples: %+v", info)
	}
	// never set and missing are ignored
	r.Observe(map[int]rega.Reading{4: {Found: true, Timestamp: 0}, 5: {}})
	if info := r.Info(4, time.Now()); info.LastUpdate != 0 {
		t.Fatalf("never set tracked: %+v", info)
	}
	r.Forget(map[int]bool{2: true})
	if info := r.Info(1, time.Now()); info.Samples != 0 {
		t.Fatal("forgotten object still tracked")
	}
	for i := int64(1); i <= 40; i++ {
		r.Observe(map[int]rega.Reading{6: {Found: true, Timestamp: base + 10*i}})
	}
	if info := r.Info(6, time.Unix(base+400, 0)); info.Samples != keepIntervals {
		t.Fatalf("kept %d intervals", info.Samples)
	}
}

func TestTexts(t *testing.T) {
	for _, r := range []Reason{Good, SourceDown, Missing, NeverSet, Unknown} {
		if r.String() == "unbekannt" {
			t.Errorf("reason %d without text", r)
		}
	}
	for c := CommNone; c <= CommUnknown; c++ {
		if c.String() == "unbekannt" {
			t.Errorf("comm %d without text", c)
		}
	}
	// Regression (ChatGPT review 0.2.3, C): neutral wording, no direction.
	if s := CommUnreach.String(); s != "CCU meldet Kommunikationsstörung (UNREACH)" {
		t.Errorf("unreach text %q", s)
	}
}
