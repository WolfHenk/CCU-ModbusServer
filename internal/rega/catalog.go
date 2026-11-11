// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/rega/catalog.go
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
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Type is the value type of a datapoint or system variable as the
// Modbus side needs it.
type Type string

const (
	Bool    Type = "BOOL"    // readable state, 0/1
	Action  Type = "ACTION"  // write-only trigger (e.g. PRESS_SHORT)
	Float   Type = "FLOAT"   // number with decimals
	Integer Type = "INTEGER" // whole number
	Enum    Type = "ENUM"    // index into a value list
	String  Type = "STRING"  // not offered (Wolfram 2026-11-11)
	Other   Type = "OTHER"   // anything else, not offered
)

// Offered reports whether values of this type can be exported.
func (t Type) Offered() bool {
	switch t {
	case Bool, Action, Float, Integer, Enum:
		return true
	}
	return false
}

// Point is one datapoint of a channel.
type Point struct {
	ReGaID    int      `json:"rega_id"`
	ID        string   `json:"id"`    // "Interface.ADDRESS:CH.PARAM", stable identity
	Param     string   `json:"param"` // PARAM
	Type      Type     `json:"type"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	ValueList []string `json:"value_list,omitempty"`
	Readable  bool     `json:"readable"`
	Writable  bool     `json:"writable"`
	Events    bool     `json:"events"`
	Value     string   `json:"value"`
	Timestamp int64    `json:"timestamp"` // Unix seconds, 0 = never set
}

// Channel is one channel of a device.
type Channel struct {
	ReGaID    int      `json:"rega_id"`
	Address   string   `json:"address"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Rooms     []string `json:"rooms,omitempty"`
	Functions []string `json:"functions,omitempty"`
	Points    []Point  `json:"points"`
}

// Device is one device with its channels.
type Device struct {
	ReGaID    int       `json:"rega_id"`
	Interface string    `json:"interface"`
	Address   string    `json:"address"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Channels  []Channel `json:"channels"`
}

// SysVar is one system variable.
type SysVar struct {
	ReGaID    int      `json:"rega_id"`
	ID        string   `json:"id"` // "SV:<ReGa ID>"
	Name      string   `json:"name"`
	Type      Type     `json:"type"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	ValueList []string `json:"value_list,omitempty"`
	Writable  bool     `json:"writable"`
	Internal  bool     `json:"internal"`
	Value     string   `json:"value"`
	Timestamp int64    `json:"timestamp"`
}

// Catalog is everything that can be exported.
type Catalog struct {
	Loaded  time.Time `json:"loaded"`
	Devices []Device  `json:"devices"`
	SysVars []SysVar  `json:"sysvars"`
}

// catalogScript lists devices, channels, datapoints, rooms, functions and
// system variables. Every text field is UriEncode()d so that '|' and line
// breaks in names cannot break the format. Objects are null-checked: a
// ReGa runtime error would abort the whole script.
const catalogScript = `string s; string c; string p; string ifn; string mn; string mx; string vl; object d; object io; object chs; object ch; object dps; object dp; object r; object f; object v;
foreach(s, dom.GetObject(ID_DEVICES).EnumIDs()) { d = dom.GetObject(s); if (d) { if (d.Address() != "") {
 ifn = ""; io = dom.GetObject(d.Interface()); if (io) { ifn = io.Name(); }
 WriteLine("D|" # d.ID() # "|" # ifn.UriEncode() # "|" # d.Address().UriEncode() # "|" # d.Name().UriEncode() # "|" # d.HssType().UriEncode());
 chs = d.Channels(); if (chs) { foreach(c, chs.EnumIDs()) { ch = dom.GetObject(c); if (ch) {
  WriteLine("C|" # ch.ID() # "|" # ch.Address().UriEncode() # "|" # ch.Name().UriEncode() # "|" # ch.HssType().UriEncode());
  dps = ch.DPs(); if (dps) { foreach(p, dps.EnumIDs()) { dp = dom.GetObject(p); if (dp) {
   mn = dp.ValueMin().ToString(); mx = dp.ValueMax().ToString(); vl = dp.Value().ToString();
   WriteLine("P|" # dp.ID() # "|" # dp.Name().UriEncode() # "|" # dp.ValueType() # "|" # dp.ValueSubType() # "|" # mn.UriEncode() # "|" # mx.UriEncode() # "|" # dp.ValueUnit().UriEncode() # "|" # dp.ValueList().UriEncode() # "|" # dp.Operations() # "|" # vl.UriEncode() # "|" # dp.Timestamp().ToInteger());
  } } }
 } } }
} } }
foreach(s, dom.GetObject(ID_ROOMS).EnumIDs()) { r = dom.GetObject(s); if (r) { foreach(c, r.EnumUsedIDs()) { WriteLine("R|" # c # "|" # r.Name().UriEncode()); } } }
foreach(s, dom.GetObject(ID_FUNCTIONS).EnumIDs()) { f = dom.GetObject(s); if (f) { foreach(c, f.EnumUsedIDs()) { WriteLine("F|" # c # "|" # f.Name().UriEncode()); } } }
foreach(s, dom.GetObject(ID_SYSTEM_VARIABLES).EnumIDs()) { v = dom.GetObject(s); if (v) {
 mn = v.ValueMin().ToString(); mx = v.ValueMax().ToString(); vl = v.Value().ToString();
 WriteLine("V|" # v.ID() # "|" # v.Name().UriEncode() # "|" # v.ValueType() # "|" # v.ValueSubType() # "|" # mn.UriEncode() # "|" # mx.UriEncode() # "|" # v.ValueUnit().UriEncode() # "|" # v.ValueList().UriEncode() # "|" # v.Operations() # "|" # vl.UriEncode() # "|" # v.Timestamp().ToInteger() # "|" # v.Internal());
} }`

// LoadCatalog reads the complete catalog from ReGa.
func (c *Client) LoadCatalog(ctx context.Context) (*Catalog, error) {
	lines, err := c.Run(ctx, catalogScript)
	if err != nil {
		return nil, err
	}
	cat, err := parseCatalog(lines)
	if err != nil {
		return nil, err
	}
	cat.Loaded = time.Now()
	return cat, nil
}

func optFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "true" || s == "false" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

func valueList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ";")
}

// classify maps ReGa ValueType and Operations to a Type.
//
//	ValueType 2 = logic, 4 = float, 8 = integer, 16 = integer/value list, 20 = string
func classify(valueType int, list []string, ops int) Type {
	switch valueType {
	case 2:
		if ops&1 == 0 && ops&2 != 0 {
			return Action
		}
		return Bool
	case 4:
		return Float
	case 8, 16:
		if len(list) > 0 {
			return Enum
		}
		return Integer
	case 20:
		return String
	}
	// Seen on a productive CCU: ValueType 6 for LEVEL of the CCU's own
	// virtual BidCoS-RF keys (write-only). Semantics unknown: not offered.
	return Other
}

func atoi(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

func parseCatalog(lines []string) (*Catalog, error) {
	cat := &Catalog{Devices: []Device{}, SysVars: []SysVar{}}
	var dev *Device
	var ch *Channel
	byChannel := map[int]*Channel{}
	type tag struct {
		id   int
		name string
	}
	var rooms, funcs []tag
	for n, line := range lines {
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		bad := func() error { return fmt.Errorf("rega catalog line %d: malformed %q", n+1, line) }
		switch f[0] {
		case "D":
			if len(f) != 6 {
				return nil, bad()
			}
			id, err := atoi(f[1])
			if err != nil {
				return nil, bad()
			}
			cat.Devices = append(cat.Devices, Device{ReGaID: id, Interface: decode(f[2]), Address: decode(f[3]), Name: decode(f[4]), Type: decode(f[5]), Channels: []Channel{}})
			dev = &cat.Devices[len(cat.Devices)-1]
			ch = nil
		case "C":
			if len(f) != 5 || dev == nil {
				return nil, bad()
			}
			id, err := atoi(f[1])
			if err != nil {
				return nil, bad()
			}
			dev.Channels = append(dev.Channels, Channel{ReGaID: id, Address: decode(f[2]), Name: decode(f[3]), Type: decode(f[4]), Points: []Point{}})
			ch = &dev.Channels[len(dev.Channels)-1]
		case "P":
			if len(f) != 12 || ch == nil {
				return nil, bad()
			}
			id, err1 := atoi(f[1])
			vt, err2 := atoi(f[3])
			ops, err3 := atoi(f[9])
			ts, err4 := strconv.ParseInt(strings.TrimSpace(f[11]), 10, 64)
			if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
				return nil, bad()
			}
			name := decode(f[2])
			list := valueList(decode(f[8]))
			param := name
			if i := strings.LastIndex(name, "."); i >= 0 {
				param = name[i+1:]
			}
			ch.Points = append(ch.Points, Point{
				ReGaID: id, ID: name, Param: param,
				Type: classify(vt, list, ops),
				Min:  optFloat(decode(f[5])), Max: optFloat(decode(f[6])),
				Unit: decode(f[7]), ValueList: list,
				Readable: ops&1 != 0, Writable: ops&2 != 0, Events: ops&4 != 0,
				Value: decode(f[10]), Timestamp: ts,
			})
		case "R", "F":
			if len(f) != 3 {
				return nil, bad()
			}
			id, err := atoi(f[1])
			if err != nil {
				return nil, bad()
			}
			t := tag{id, decode(f[2])}
			if f[0] == "R" {
				rooms = append(rooms, t)
			} else {
				funcs = append(funcs, t)
			}
		case "V":
			if len(f) != 13 {
				return nil, bad()
			}
			id, err1 := atoi(f[1])
			vt, err2 := atoi(f[3])
			ops, err3 := atoi(f[9])
			ts, err4 := strconv.ParseInt(strings.TrimSpace(f[11]), 10, 64)
			if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
				return nil, bad()
			}
			list := valueList(decode(f[8]))
			t := classify(vt, list, ops|1) // system variables are always readable
			cat.SysVars = append(cat.SysVars, SysVar{
				ReGaID: id, ID: "SV:" + strconv.Itoa(id), Name: decode(f[2]),
				Type: t,
				Min:  optFloat(decode(f[5])), Max: optFloat(decode(f[6])),
				Unit: decode(f[7]), ValueList: list,
				Writable: ops&2 != 0, Internal: strings.TrimSpace(f[12]) == "true",
				Value: decode(f[10]), Timestamp: ts,
			})
		default:
			return nil, bad()
		}
	}
	// Channel pointers are stable only after all devices are appended.
	for i := range cat.Devices {
		for j := range cat.Devices[i].Channels {
			c := &cat.Devices[i].Channels[j]
			byChannel[c.ReGaID] = c
		}
	}
	for _, r := range rooms {
		if c := byChannel[r.id]; c != nil {
			c.Rooms = append(c.Rooms, r.name)
		}
	}
	for _, r := range funcs {
		if c := byChannel[r.id]; c != nil {
			c.Functions = append(c.Functions, r.name)
		}
	}
	return cat, nil
}
