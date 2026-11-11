// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/source/source.go
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

// Package source defines the data source the server works against. The
// ReGa adapter (package rega) is the only implementation today; an
// event-based XML-RPC adapter can be added later behind the same
// interface without changing the Modbus side or the register addresses.
package source

import (
	"context"

	"github.com/WolfHenk/CCU-ModbusServer/internal/rega"
)

// DataSource is everything the server needs from the CCU.
type DataSource interface {
	// LoadCatalog returns all devices, channels, datapoints and system
	// variables with their metadata.
	LoadCatalog(ctx context.Context) (*rega.Catalog, error)
	// ReadValues returns exactly one reading per requested object, or an
	// error; partial answers are errors.
	ReadValues(ctx context.Context, ids []int) (map[int]rega.Reading, error)
	// SetValue writes one typed value. Success means the source accepted
	// the command, not that a device executed it.
	SetValue(ctx context.Context, id int, t rega.Type, v float64) error
	// Ping checks that the source answers.
	Ping(ctx context.Context) error
}

var _ DataSource = (*rega.Client)(nil)
