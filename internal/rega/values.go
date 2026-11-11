// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/rega/values.go
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
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Reading is the ReGa cache state of one object.
type Reading struct {
	Found     bool
	Value     string
	Timestamp int64
}

// ReadValues reads the current ReGa values of the given object IDs in
// one script. IDs that no longer exist are returned with Found=false.
// The answer must contain exactly one line per requested ID and nothing
// else; otherwise the whole read is an error and must not update any
// cache. Reading from ReGa never causes radio traffic.
func (c *Client) ReadValues(ctx context.Context, ids []int) (map[int]Reading, error) {
	want := make(map[int]bool, len(ids))
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("rega values: invalid id %d", id)
		}
		if !want[id] {
			want[id] = true
			parts = append(parts, strconv.Itoa(id))
		}
	}
	out := make(map[int]Reading, len(want))
	if len(want) == 0 {
		return out, nil
	}
	// Variables are declared once outside the loop: ReGa does not allow
	// declarations inside loop bodies, and an assignment keeps the type of
	// the assigned value, hence ToString(). ReGa iterates a tab separated
	// string list.
	script := `string s; string v; object o; foreach(s, "` + strings.Join(parts, "\t") + `") { o = dom.GetObject(s); if (o) { v = o.Value().ToString(); WriteLine(s # "|" # v.UriEncode() # "|" # o.Timestamp().ToInteger()); } else { WriteLine(s # "|-|0"); } }`
	lines, err := c.Run(ctx, script)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != 3 {
			return nil, fmt.Errorf("rega values: malformed %q", line)
		}
		id, err := strconv.Atoi(f[0])
		if err != nil || !want[id] {
			return nil, fmt.Errorf("rega values: unexpected id in %q", line)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("rega values: duplicate id %d", id)
		}
		ts, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || ts < 0 {
			return nil, fmt.Errorf("rega values: bad timestamp in %q", line)
		}
		if f[1] == "-" {
			if ts != 0 {
				return nil, fmt.Errorf("rega values: malformed %q", line)
			}
			out[id] = Reading{}
			continue
		}
		out[id] = Reading{Found: true, Value: decode(f[1]), Timestamp: ts}
	}
	if len(out) != len(want) {
		return nil, fmt.Errorf("rega values: %d of %d ids answered", len(out), len(want))
	}
	return out, nil
}

// ErrRejected is returned when ReGa did not accept a value.
var ErrRejected = errors.New("rega: value not accepted")

// Literal returns the ReGa script literal for a value of type t, or an
// error. Only plain numbers and true/false are produced, so no script
// text from outside can reach ReGa.
func Literal(t Type, v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", fmt.Errorf("invalid number")
	}
	switch t {
	case Bool, Action:
		if v != 0 {
			return "true", nil
		}
		return "false", nil
	case Integer, Enum:
		if v != math.Trunc(v) || math.Abs(v) > 1<<31 {
			return "", fmt.Errorf("not an integer: %v", v)
		}
		return strconv.FormatInt(int64(v), 10), nil
	case Float:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("type %s cannot be written", t)
}

// SetValue sets one object of type t through ReGa, like the WebUI does
// (programs triggered by the value run as usual). The value is turned
// into a script literal by Literal, so only finite plain numbers and
// true/false reach ReGa.
//
// Success means: the object exists and ReGa's State() returned true.
// Verified on OpenCCU 3.89.11: State() returns false for a read-only
// datapoint, but ReGa does NOT check MIN/MAX (a system variable with
// max 100 accepted 5000). Range checks are the caller's job, and success
// does not mean the device has executed the command; the value read back
// later is the authority.
func (c *Client) SetValue(ctx context.Context, id int, t Type, v float64) error {
	if id <= 0 {
		return fmt.Errorf("rega: invalid id %d", id)
	}
	lit, err := Literal(t, v)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`object o = dom.GetObject(%d); if (o) { if (o.State(%s)) { WriteLine("OK"); } else { WriteLine("REJECTED"); } } else { WriteLine("MISSING"); }`, id, lit)
	lines, err := c.Run(ctx, script)
	if err != nil {
		return err
	}
	if len(lines) == 1 && lines[0] == "OK" {
		return nil
	}
	return ErrRejected
}
