// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/quality/quality.go
// Ersteller:    Wolfram Henkel
// Datum:        11. Nov. 2026
// Version:      0.2.2
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

// Package quality decides whether an exported value is valid and, if not,
// why. The reasons are kept apart so that the client sheet and the API can
// tell "never received" from "device unreachable" from "CCU not answering".
//
// What is deliberately NOT a reason: the age of the ReGa timestamp. Most
// Homematic datapoints change only on events (a switch state may be days
// old and still correct), so a device timestamp says nothing about
// staleness. Staleness is judged by the age of the last successful read
// from ReGa instead (SourceDown).
//
// STICKY_UNREACH is not offered by ReGa (checked on a productive CCU with
// 109 devices) and is therefore not used.
package quality

import (
	"strings"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

// Reason is why a value is (in)valid. Lower numbers win when several
// reasons apply; Good is the only valid one.
type Reason uint8

const (
	Good       Reason = 0 // valid
	SourceDown Reason = 1 // no successful ReGa read for too long
	Missing    Reason = 2 // object no longer exists in ReGa
	Unreach    Reason = 3 // device reports UNREACH = true
	NeverSet   Reason = 4 // ReGa timestamp 0: no confirmed value yet
	Unknown    Reason = 5 // object or its UNREACH state not polled (yet)
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
	case Unreach:
		return "Gerät nicht erreichbar"
	case NeverSet:
		return "noch kein Wert empfangen"
	case Unknown:
		return "noch nicht gelesen / Erreichbarkeit unbekannt"
	}
	return "unbekannt"
}

// Valid reports whether the reason means a valid value.
func (r Reason) Valid() bool { return r == Good }

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

// Judge decides the quality of one object.
//
//	id        ReGa ID of the object
//	unreachID ReGa ID of its device's UNREACH datapoint, 0 if none
//	values    last successful poll result
//	lastOK    time of the last successful poll
//	maxAge    how old lastOK may be (e.g. 3 x interval + timeout)
func Judge(id, unreachID int, values map[int]rega.Reading, lastOK time.Time, maxAge time.Duration, now time.Time) Reason {
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
	if unreachID != 0 {
		// The device has an UNREACH datapoint: its state must be known.
		// Not read yet or gone -> the reachability is unknown, so the
		// value is not declared valid.
		u, ok := values[unreachID]
		if !ok || !u.Found {
			return Unknown
		}
		if u.Value != "false" {
			return Unreach
		}
	}
	if r.Timestamp == 0 {
		return NeverSet
	}
	return Good
}

// PollIDs returns the exported IDs plus the UNREACH IDs of their devices,
// so that Judge always has the reachability it needs.
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
