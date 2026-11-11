// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/quality/quality.go
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

// Package quality describes the state of an exported value as three
// SEPARATE pieces of information (Wolfram, 2026-11-11):
//
//  1. Value quality (Judge): is there a value from a working read of the
//     CCU logic? CCU logic not answering, object gone, never set, not
//     read yet. UNREACH does NOT make a value invalid: in practice
//     (Wolfram, Bad-oben-Ventil) a device can be UNREACH because commands
//     are not acknowledged while its measured values still arrive.
//  2. Communication warning (Comm): the device's UNREACH datapoint
//     (channel 0), reported as a warning of its own.
//  3. Reception (Reception): when ReGa last received an update for the
//     object and how often that typically happens for this object. Only
//     changes of the ReGa timestamp count as reception; reading an
//     unchanged cached value again is not a new radio message. Without
//     enough observations the answer is "unknown", never a guess.
//
// STICKY_UNREACH is not offered by ReGa (checked on a productive CCU with
// 109 devices) and is not used.
package quality

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

// Reason is the value quality. Good is the only valid one.
type Reason uint8

const (
	Good       Reason = 0 // value present from a working read
	SourceDown Reason = 1 // no successful ReGa read for too long
	Missing    Reason = 2 // object no longer exists in ReGa
	NeverSet   Reason = 4 // ReGa timestamp 0: no value received yet
	Unknown    Reason = 5 // object not read yet
)

// String returns a short German text for the WebUI and the client sheet.
func (r Reason) String() string {
	switch r {
	case Good:
		return "gültig"
	case SourceDown:
		return "CCU-Logik antwortet nicht"
	case Missing:
		return "Objekt in der CCU nicht mehr vorhanden"
	case NeverSet:
		return "noch kein Wert empfangen"
	case Unknown:
		return "noch nicht gelesen"
	}
	return "unbekannt"
}

// Valid reports whether the reason means a valid value.
func (r Reason) Valid() bool { return r == Good }

// Judge decides the value quality of one object.
//
//	values  last successful poll result
//	lastOK  time of the last successful poll
//	maxAge  how old lastOK may be (e.g. 3 x interval + timeout)
func Judge(id int, values map[int]rega.Reading, lastOK time.Time, maxAge time.Duration, now time.Time) Reason {
	if lastOK.IsZero() || now.Sub(lastOK) > maxAge {
		return SourceDown
	}
	r, ok := values[id]
	if !ok {
		return Unknown
	}
	if !r.Found {
		return Missing
	}
	if r.Timestamp == 0 {
		return NeverSet
	}
	return Good
}

// Comm is the communication warning derived from UNREACH.
type Comm uint8

const (
	CommNone    Comm = 0 // device has no UNREACH datapoint (e.g. system variable)
	CommOK      Comm = 1 // UNREACH = false
	CommUnreach Comm = 2 // UNREACH = true (or any value other than false)
	CommUnknown Comm = 3 // UNREACH exists but is not read or gone
)

// String returns a short German text.
func (c Comm) String() string {
	switch c {
	case CommNone:
		return "keine Angabe"
	case CommOK:
		return "erreichbar"
	case CommUnreach:
		return "Warnung: Gerät meldet „nicht erreichbar“ (Senden/Quittung gestört)"
	case CommUnknown:
		return "Erreichbarkeit unbekannt"
	}
	return "unbekannt"
}

// Warning reports whether the state should be flagged to the client.
func (c Comm) Warning() bool { return c == CommUnreach || c == CommUnknown }

// CommState returns the communication warning for a device's UNREACH
// datapoint (unreachID 0 = device has none).
func CommState(unreachID int, values map[int]rega.Reading) Comm {
	if unreachID == 0 {
		return CommNone
	}
	u, ok := values[unreachID]
	if !ok || !u.Found {
		return CommUnknown
	}
	if u.Value == "false" {
		return CommOK
	}
	return CommUnreach
}

// UnreachIDs maps the ReGa ID of every datapoint to the ReGa ID of the
// UNREACH datapoint of its device (channel 0), where one exists.
func UnreachIDs(cat *rega.Catalog) map[int]int {
	out := map[int]int{}
	if cat == nil {
		return out
	}
	for _, d := range cat.Devices {
		un := 0
		for _, ch := range d.Channels {
			if !strings.HasSuffix(ch.Address, ":0") {
				continue
			}
			for _, p := range ch.Points {
				if p.Param == "UNREACH" {
					un = p.ReGaID
				}
			}
		}
		if un == 0 {
			continue
		}
		for _, ch := range d.Channels {
			for _, p := range ch.Points {
				out[p.ReGaID] = un
			}
		}
	}
	return out
}

// PollIDs returns the exported IDs plus the UNREACH IDs of their devices.
func PollIDs(exported []int, unreach map[int]int) []int {
	seen := map[int]bool{}
	var out []int
	add := func(id int) {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range exported {
		add(id)
		add(unreach[id])
	}
	return out
}

// Tri is a three-valued answer.
type Tri uint8

const (
	TriUnknown Tri = 0
	TriNo      Tri = 1
	TriYes     Tri = 2
)

// Reception settings.
const (
	keepIntervals = 16 // observed update intervals kept per object
	minSamples    = 4  // intervals needed before "overdue" is judged
	overdueFactor = 3  // overdue when silent for > factor x typical interval
)

// ReceptionInfo describes the observed updates of one object.
type ReceptionInfo struct {
	LastUpdate int64 `json:"last_update"` // ReGa timestamp, Unix s, 0 = none
	Typical    int64 `json:"typical_s"`   // median interval in s, 0 = not enough data
	Samples    int   `json:"samples"`     // number of observed intervals
	Overdue    Tri   `json:"overdue"`     // 0 unknown, 1 no, 2 yes
}

type track struct {
	last      int64
	intervals []int64
}

// Reception learns, per object, how often ReGa receives updates. It is
// an observation, not a guarantee: the typical interval is the median of
// the last observed intervals since the add-on started, and "overdue" is
// only stated after enough observations. Objects that change only on
// events (switches, contacts) usually never reach "overdue: yes" — which
// is correct, because silence is normal for them.
type Reception struct {
	mu sync.Mutex
	m  map[int]*track
}

// NewReception creates an empty tracker.
func NewReception() *Reception { return &Reception{m: map[int]*track{}} }

// Observe feeds the result of a successful poll cycle.
func (r *Reception) Observe(values map[int]rega.Reading) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range values {
		if !v.Found || v.Timestamp <= 0 {
			continue
		}
		t := r.m[id]
		if t == nil {
			r.m[id] = &track{last: v.Timestamp}
			continue
		}
		if v.Timestamp > t.last {
			t.intervals = append(t.intervals, v.Timestamp-t.last)
			if len(t.intervals) > keepIntervals {
				t.intervals = t.intervals[1:]
			}
			t.last = v.Timestamp
		}
	}
}

// Forget drops objects that are no longer polled.
func (r *Reception) Forget(keep map[int]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.m {
		if !keep[id] {
			delete(r.m, id)
		}
	}
}

// Info returns what is known about the updates of one object.
func (r *Reception) Info(id int, now time.Time) ReceptionInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.m[id]
	if t == nil {
		return ReceptionInfo{}
	}
	info := ReceptionInfo{LastUpdate: t.last, Samples: len(t.intervals)}
	if len(t.intervals) < minSamples {
		return info
	}
	s := append([]int64(nil), t.intervals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	info.Typical = s[len(s)/2]
	if now.Unix()-t.last > overdueFactor*info.Typical {
		info.Overdue = TriYes
	} else {
		info.Overdue = TriNo
	}
	return info
}
