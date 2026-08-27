// SPDX-License-Identifier: Apache-2.0

// Package ui contains lightweight terminal output helpers.
package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const (
	teal   = "\x1b[38;2;12;109;119m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	red    = "\x1b[31m"
	reset  = "\x1b[0m"
)

// Printer keeps machine-readable output on stdout and human output on stderr.
type Printer struct {
	Data    io.Writer
	Human   io.Writer
	JSON    bool
	Quiet   bool
	NoColor bool
}

// Encode writes one JSON document to the data stream.
func (p Printer) Encode(value any) error {
	encoder := json.NewEncoder(p.Data)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}

// Success writes a successful human message unless quiet mode is active.
func (p Printer) Success(format string, args ...any) {
	if p.JSON || p.Quiet {
		return
	}
	fmt.Fprintf(p.Human, "%s✓%s %s\n", p.color(green), p.color(reset), fmt.Sprintf(format, args...))
}

// Info writes a human informational message unless quiet mode is active.
func (p Printer) Info(format string, args ...any) {
	if p.JSON || p.Quiet {
		return
	}
	fmt.Fprintf(p.Human, "%s•%s %s\n", p.color(teal), p.color(reset), fmt.Sprintf(format, args...))
}

// Warn writes a human warning.
func (p Printer) Warn(format string, args ...any) {
	if p.JSON {
		return
	}
	fmt.Fprintf(p.Human, "%s!%s %s\n", p.color(yellow), p.color(reset), fmt.Sprintf(format, args...))
}

// Error writes a human error.
func (p Printer) Error(format string, args ...any) {
	if p.JSON {
		return
	}
	fmt.Fprintf(p.Human, "%s✗%s %s\n", p.color(red), p.color(reset), fmt.Sprintf(format, args...))
}

func (p Printer) color(code string) string {
	if p.NoColor || !isTerminal(p.Human) {
		return ""
	}
	return code
}

func isTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
