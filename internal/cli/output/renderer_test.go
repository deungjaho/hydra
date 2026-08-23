package output

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestWriteSuccessJSON(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(FormatJSON, WithStdout(&buf), WithStderr(&buf))
	data := map[string]any{"id": 1, "email": "test@example.com"}
	if err := r.WriteSuccess(data, nil); err != nil {
		t.Fatalf("WriteSuccess: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if parsed["schema_version"] != float64(1) {
		t.Errorf("schema_version = %v, want 1", parsed["schema_version"])
	}
	d, ok := parsed["data"].(map[string]any)
	if !ok {
		t.Fatal("data field missing or wrong type")
	}
	if d["email"] != "test@example.com" {
		t.Errorf("email = %v", d["email"])
	}
}

func TestWriteErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(FormatJSON, WithStdout(&buf), WithStderr(&buf))
	if err := r.WriteError("NOT_FOUND", "account 3 was not found", false, nil); err != nil {
		t.Fatalf("WriteError: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	e, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatal("error field missing")
	}
	if e["code"] != "NOT_FOUND" {
		t.Errorf("code = %v, want NOT_FOUND", e["code"])
	}
	if e["message"] != "account 3 was not found" {
		t.Errorf("message = %v", e["message"])
	}
}

func TestWriteSuccessTable(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(FormatTable, WithStdout(&buf), WithStderr(&buf))
	if err := r.WriteSuccess(nil, func(w io.Writer) error {
		_, _ = w.Write([]byte("table output\n"))
		return nil
	}); err != nil {
		t.Fatalf("WriteSuccess: %v", err)
	}
	if !strings.Contains(buf.String(), "table output") {
		t.Errorf("table output missing: %q", buf.String())
	}
}

func TestWriteErrorTable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := NewRenderer(FormatTable, WithStdout(&stdout), WithStderr(&stderr))
	if err := r.WriteError("NOT_FOUND", "account not found", false, nil); err != nil {
		t.Fatalf("WriteError: %v", err)
	}
	if !strings.Contains(stderr.String(), "account not found") {
		t.Errorf("stderr should contain error message: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on error, got %q", stdout.String())
	}
}

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	headers := []string{"ID", "EMAIL"}
	rows := [][]string{{"1", "a@test"}, {"2", "b@test"}}
	if err := PrintTable(&buf, headers, rows); err != nil {
		t.Fatalf("PrintTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ID") {
		t.Error("headers missing")
	}
	if !strings.Contains(out, "a@test") {
		t.Error("row data missing")
	}
}
