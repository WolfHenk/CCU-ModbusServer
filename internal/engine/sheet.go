// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/engine/sheet.go
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

package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/WolfHenk/CCU-ModbusServer/internal/mapping"
	"github.com/WolfHenk/CCU-ModbusServer/internal/quality"
	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
	"github.com/WolfHenk/CCU-ModbusServer/internal/table"
)

// SheetRow is one line of the client sheet (register list).
type SheetRow struct {
	Export    int                   `json:"export"`
	Label     string                `json:"label"`
	Key       string                `json:"key"`
	Source    string                `json:"source"` // datapoint ID or system variable name
	Table     string                `json:"table"`
	FCRead    string                `json:"fc_read"`
	FCWrite   string                `json:"fc_write,omitempty"`
	Address   int                   `json:"address"` // 0-based protocol address
	Classic   string                `json:"classic"` // 0xxxx/1xxxx/3xxxx/4xxxx, 1-based
	Count     int                   `json:"count"`   // registers or bits
	Format    string                `json:"format"`  // bit, int16, ..., float32
	Scale     float64               `json:"scale"`   // register = value x scale
	Unit      string                `json:"unit"`
	Min       *float64              `json:"min,omitempty"`
	Max       *float64              `json:"max,omitempty"`
	ValueList []string              `json:"value_list,omitempty"`
	Access    string                `json:"access"` // "R" or "R/W"
	ValidBit  int                   `json:"valid_bit"`
	WarnBit   int                   `json:"warn_bit"`
	Value     string                `json:"value"`  // current CCU value
	Valid     bool                  `json:"valid"`  // value quality
	Reason    string                `json:"reason"` // text of the value quality
	Comm      string                `json:"comm"`   // communication warning text
	Reception quality.ReceptionInfo `json:"reception"`
	Orphan    bool                  `json:"orphan"`
	Reserved  bool                  `json:"reserved"` // deselected: address and bits stay reserved
}

func classic(a table.Area, addr int) string {
	prefix := map[table.Area]int{table.Coils: 0, table.DiscreteInputs: 1, table.InputRegisters: 3, table.HoldingRegisters: 4}[a]
	return fmt.Sprintf("%d%05d", prefix, addr+1)
}

func fcs(a table.Area, n int) (string, string) {
	switch a {
	case table.Coils:
		if n > 1 {
			return "01", "15"
		}
		return "01", "05/15"
	case table.DiscreteInputs:
		return "02", ""
	case table.HoldingRegisters:
		if n > 1 {
			return "03", "16"
		}
		return "03", "06/16"
	}
	return "04", ""
}

// Sheet returns the client sheet rows sorted by table and address.
func (e *Engine) Sheet(now time.Time) []SheetRow {
	values, h := e.pol.Values()
	e.mu.Lock()
	defer e.mu.Unlock()
	var rows []SheetRow
	for _, o := range e.objs {
		en := o.entry
		n := max(1, en.Format.Words())
		r, w := fcs(en.Area, n)
		row := SheetRow{
			Export: en.Export, Label: en.Label, Key: en.Key, Source: o.meta.pointName,
			Table: en.Area.String(), FCRead: r, FCWrite: w,
			Address: en.Address, Classic: classic(en.Area, en.Address), Count: n,
			Format: string(en.Format), Scale: en.Scale, Unit: o.meta.unit,
			Min: o.meta.min, Max: o.meta.max, ValueList: o.meta.list,
			Access:   "R",
			ValidBit: mapping.ValidBase + en.Export, WarnBit: mapping.WarnBase + en.Export,
			Orphan: en.Orphan,
		}
		if en.Writable && !en.Orphan {
			row.Access = "R/W"
		} else {
			row.FCWrite = ""
		}
		reason := quality.Missing
		if o.regaID != 0 {
			reason = quality.Judge(o.regaID, values, h.LastOK, e.MaxAge, now)
			if e.isStale(o.regaID) {
				reason = quality.Unknown
			}
			row.Value = values[o.regaID].Value
			row.Reception = e.rec.Info(o.regaID, now)
		}
		row.Valid, row.Reason = reason.Valid(), reason.String()
		if o.regaID != 0 {
			row.Comm = quality.CommState(o.unreach, values).String()
		}
		if en.Type == rega.Action {
			row.Value = ""
		}
		rows = append(rows, row)
	}
	// Deselected entries keep their addresses; the client must know them.
	for _, en := range e.m.Entries {
		if !en.Removed {
			continue
		}
		n := max(1, en.Format.Words())
		src := en.SysVarName
		if en.Kind == "dp" {
			src = strings.TrimPrefix(en.Key, "dp:")
		}
		rows = append(rows, SheetRow{
			Export: en.Export, Label: en.Label, Key: en.Key, Source: src,
			// no function codes: the address is inactive (reads give the
			// placeholder value with valid bit 0), only reserved
			Table:   en.Area.String(),
			Address: en.Address, Classic: classic(en.Area, en.Address), Count: n,
			Format: string(en.Format), Scale: en.Scale, Access: "–",
			ValidBit: mapping.ValidBase + en.Export, WarnBit: mapping.WarnBase + en.Export,
			Reason: "reserviert (abgewählt)", Comm: quality.CommNone.String(), Reserved: true,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Table != rows[j].Table {
			return order(rows[i].Table) < order(rows[j].Table)
		}
		return rows[i].Address < rows[j].Address
	})
	return rows
}

func order(t string) int {
	switch t {
	case table.Coils.String():
		return 0
	case table.DiscreteInputs.String():
		return 1
	case table.InputRegisters.String():
		return 2
	}
	return 3
}
