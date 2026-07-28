package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// Format is the output format for CLI commands.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
)

// Renderer writes data in the configured format (table or JSON).
// JSON output is a single complete document with schema_version.
// Table output is human-readable and may vary between versions.
type Renderer struct {
	Format    Format
	NoColor   bool
	IsTTY     bool
	SchemaVer int
	stdout    io.Writer
	stderr    io.Writer
}

// NewRenderer creates a Renderer. stdout/stderr default to os.Stdout/os.Stderr.
func NewRenderer(format Format, opts ...RendererOption) *Renderer {
	r := &Renderer{
		Format:    format,
		IsTTY:     isatty(os.Stdout),
		SchemaVer: 1,
		stdout:    os.Stdout,
		stderr:    os.Stderr,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

type RendererOption func(*Renderer)

func WithNoColor(b bool) RendererOption     { return func(r *Renderer) { r.NoColor = b } }
func WithTTY(b bool) RendererOption         { return func(r *Renderer) { r.IsTTY = b } }
func WithStdout(w io.Writer) RendererOption { return func(r *Renderer) { r.stdout = w } }
func WithStderr(w io.Writer) RendererOption { return func(r *Renderer) { r.stderr = w } }

// WriteSuccess writes a successful data response. In JSON mode, it
// outputs {"schema_version":N,"data":...,"warnings":[]}. In table mode,
// it calls the tableFn to render a human-readable view.
func (r *Renderer) WriteSuccess(data any, tableFn func(w io.Writer) error) error {
	if r.Format == FormatJSON {
		return r.writeJSON(data, nil)
	}
	if tableFn != nil {
		return tableFn(r.stdout)
	}
	return nil
}

// WriteError writes an error response. In JSON mode, it outputs
// {"schema_version":N,"error":{"code":...,"message":...,"retryable":...}}.
// In table mode, it writes the message to stderr.
func (r *Renderer) WriteError(code, message string, retryable bool, details any) error {
	if r.Format == FormatJSON {
		errObj := map[string]any{
			"code":      code,
			"message":   message,
			"retryable": retryable,
		}
		if details != nil {
			errObj["details"] = details
		}
		return r.writeJSON(nil, errObj)
	}
	_, err := fmt.Fprintln(r.stderr, message)
	return err
}

// writeJSON writes the unified JSON envelope to stdout.
func (r *Renderer) writeJSON(data any, errObj any) error {
	out := map[string]any{
		"schema_version": r.SchemaVer,
	}
	if errObj != nil {
		out["error"] = errObj
	} else {
		out["data"] = data
		out["warnings"] = []any{}
	}
	enc := json.NewEncoder(r.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// PrintTable is a helper that writes aligned columns to w.
func PrintTable(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	// Header
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

// isatty returns true if w is a terminal. We use a simple check —
// the caller can override via WithTTY.
func isatty(w *os.File) bool {
	fi, err := w.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
