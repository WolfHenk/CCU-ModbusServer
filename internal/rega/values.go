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
// Reading from ReGa never causes radio traffic.
func (c *Client) ReadValues(ctx context.Context, ids []int) (map[int]Reading, error) {
	out := make(map[int]Reading, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	// ReGa iterates a tab separated string list.
	// Variables are declared once outside the loop: ReGa does not allow
	// declarations inside loop bodies, and an assignment keeps the type of
	// the assigned value, hence ToString().
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
		id, err := atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("rega values: malformed %q", line)
		}
		if f[1] == "-" {
			out[id] = Reading{}
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		out[id] = Reading{Found: true, Value: decode(f[1]), Timestamp: ts}
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

// SetValue sets one object through ReGa, like the WebUI does (programs
// triggered by the value run as usual). The literal must come from
// Literal.
func (c *Client) SetValue(ctx context.Context, id int, literal string) error {
	if _, err := strconv.ParseFloat(literal, 64); err != nil && literal != "true" && literal != "false" {
		return fmt.Errorf("rega: invalid literal %q", literal)
	}
	script := fmt.Sprintf(`object o = dom.GetObject(%d); if (o) { o.State(%s); WriteLine("OK"); } else { WriteLine("MISSING"); }`, id, literal)
	lines, err := c.Run(ctx, script)
	if err != nil {
		return err
	}
	if len(lines) == 1 && lines[0] == "OK" {
		return nil
	}
	return ErrRejected
}
