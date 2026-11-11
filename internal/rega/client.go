// -----------------------------------------------------------------------------
// Projekt:      CCU-ModbusServer
// Datei:        internal/rega/client.go
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

// Package rega talks to the ReGa logic layer of the CCU through its local
// script interface (http://127.0.0.1:8181/rega.exe). It reads the device
// and system variable catalog, reads values from the ReGa cache (no radio
// traffic) and sets values the same way the WebUI does.
package rega

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultURL is the local ReGa script interface of OpenCCU.
const DefaultURL = "http://127.0.0.1:8181/rega.exe"

// endMarker is written by every script as its last line. A missing marker
// means the script was aborted by a ReGa runtime error.
const endMarker = "#END#"

// Client runs ReGa scripts.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient returns a client with a 10 s timeout.
func NewClient(u string) *Client {
	if u == "" {
		u = DefaultURL
	}
	return &Client{URL: u, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// ErrAborted is returned when a script did not reach its end marker.
var ErrAborted = errors.New("rega: script aborted (no end marker)")

// Run executes a script and returns its output lines without the end
// marker. The script must not write the end marker itself; Run appends it.
func (c *Client) Run(ctx context.Context, script string) ([]string, error) {
	body := script + "\nWriteLine(\"" + endMarker + "\");\n"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rega: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return splitOutput(raw)
}

// splitOutput removes the trailing <xml> variable dump that rega.exe
// appends and checks for the end marker.
func splitOutput(raw []byte) ([]string, error) {
	if i := bytes.LastIndex(raw, []byte("<xml>")); i >= 0 {
		raw = raw[:i]
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == endMarker {
			return lines[:i], nil
		}
		if lines[i] != "" {
			break
		}
	}
	return nil, ErrAborted
}

// decode reverses ReGa's UriEncode(). ReGa keeps names in ISO-8859-1;
// byte sequences that are not valid UTF-8 are converted from Latin-1.
func decode(s string) string {
	b, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	if utf8.ValidString(b) {
		return b
	}
	r := make([]rune, 0, len(b))
	for i := 0; i < len(b); i++ {
		r = append(r, rune(b[i]))
	}
	return string(r)
}
