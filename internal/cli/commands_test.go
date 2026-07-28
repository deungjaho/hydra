package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/deungjaho/hydra/internal/app"
	"github.com/deungjaho/hydra/internal/cli/output"
)

// executeCmd runs a cobra command with the given args and captures
// stdout, stderr and the exit code that Run() would produce. It
// replicates Run()'s error rendering logic so tests can verify the
// actual output that users would see.
func executeCmd(t *testing.T, args []string) (stdout, stderr string, exitCode int) {
	t.Helper()

	// Capture stdout via pipe.
	oldStdout := os.Stdout
	rOut, wOut, _ := os.Pipe()
	os.Stdout = wOut

	// Capture stderr via pipe.
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	// Build a fresh root command for each test.
	root := NewRootCmd()
	root.SetArgs(args)

	// Execute in a goroutine so we can read pipes concurrently.
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		err := root.Execute()
		// Render errors while pipes are still in place, so error
		// output is also captured. This mirrors what Run() does.
		if err != nil {
			formatStr, _ := root.Flags().GetString("output")
			isJSON := formatStr == "json"

			var appErr *app.AppError
			if errors.As(err, &appErr) {
				if isJSON {
					r := output.NewRenderer(output.FormatJSON)
					_ = r.WriteError(string(appErr.Code), appErr.Message, appErr.Retryable, appErr.Details)
				} else {
					fmt.Fprintln(os.Stderr, appErr.Error())
				}
			} else if isUsageError(err, root) {
				if isJSON {
					r := output.NewRenderer(output.FormatJSON)
					_ = r.WriteError("INVALID_ARGUMENT", err.Error(), false, nil)
				} else {
					fmt.Fprintln(os.Stderr, err.Error())
				}
			} else {
				if isJSON {
					r := output.NewRenderer(output.FormatJSON)
					_ = r.WriteError("INTERNAL", err.Error(), false, nil)
				} else {
					fmt.Fprintln(os.Stderr, err.Error())
				}
			}
		}
		wOut.Close()
		wErr.Close()
		done <- result{err}
	}()

	var outBuf, errBuf bytes.Buffer
	io.Copy(&outBuf, rOut)
	io.Copy(&errBuf, rErr)
	res := <-done

	os.Stdout = oldStdout
	os.Stderr = oldStderr

	stdout = outBuf.String()
	stderr = errBuf.String()
	err := res.err

	// Classify exit code.
	if err == nil {
		return stdout, stderr, 0
	}
	var appErr *app.AppError
	if errors.As(err, &appErr) {
		return stdout, stderr, appErr.ExitCode()
	}
	if isUsageError(err, root) {
		return stdout, stderr, app.ExitUsage
	}
	return stdout, stderr, app.ExitGeneric
}

// setupTempDB creates a temp DB by redirecting XDG_CONFIG_HOME.
func setupTempDB(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	oldEnv := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	_ = filepath.Join(dir, "hydra.db")
	return func() {
		os.Setenv("XDG_CONFIG_HOME", oldEnv)
	}
}

func TestVersionCommand(t *testing.T) {
	stdout, stderr, code := executeCmd(t, []string{"version"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty, got %q", stderr)
	}
	if !strings.Contains(stdout, "hydra") {
		t.Errorf("stdout should contain version, got %q", stdout)
	}
}

func TestVersionCommandJSON(t *testing.T) {
	stdout, _, code := executeCmd(t, []string{"--output", "json", "version"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if parsed["schema_version"] == nil {
		t.Error("schema_version missing")
	}
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		t.Fatal("data field missing or wrong type")
	}
	if data["version"] == nil {
		t.Error("version field missing in data")
	}
}

func TestVersionFlag(t *testing.T) {
	stdout, _, code := executeCmd(t, []string{"--version"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "hydra") {
		t.Errorf("stdout should contain version, got %q", stdout)
	}
}

func TestInvalidOutputFormat(t *testing.T) {
	_, stderr, code := executeCmd(t, []string{"--output", "yaml", "version"})
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (usage error)", code)
	}
	if stderr == "" {
		t.Error("stderr should contain error message for invalid output format")
	}
}

func TestUnknownCommand(t *testing.T) {
	_, stderr, code := executeCmd(t, []string{"nonexistent-command"})
	if code == 0 {
		t.Error("exit code should be non-zero for unknown command")
	}
	if stderr == "" {
		t.Error("stderr should contain error for unknown command")
	}
}

func TestStatusCommandJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "status"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if parsed["schema_version"] == nil {
		t.Error("schema_version missing")
	}
}

func TestAccountsListJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "accounts", "list"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if parsed["schema_version"] == nil {
		t.Error("schema_version missing")
	}
	data, ok := parsed["data"].([]any)
	if !ok {
		t.Fatal("data should be an array")
	}
	// Empty DB → empty array, not nil.
	if data == nil {
		t.Error("data should be an empty array, not nil")
	}
}

func TestKeyListJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "key", "list"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	data, ok := parsed["data"].([]any)
	if !ok {
		t.Fatal("data should be an array")
	}
	_ = data
}

func TestKeyAddJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "key", "add", "test-label"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		t.Fatal("data should be an object")
	}
	// Full key should be present on creation.
	if data["full_key"] == nil || data["full_key"] == "" {
		t.Error("full_key should be populated on creation")
	}
}

func TestKeyRemoveNotFound(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	_, stderr, code := executeCmd(t, []string{"key", "remove", "999"})
	if code == 0 {
		t.Error("exit code should be non-zero for not-found key")
	}
	if stderr == "" {
		t.Error("stderr should contain error message")
	}
}

func TestKeyRemoveNotFoundJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "key", "remove", "999"})
	if code == 0 {
		t.Error("exit code should be non-zero for not-found key")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatal("error field missing in JSON output")
	}
	if errObj["code"] != "NOT_FOUND" {
		t.Errorf("error code = %v, want NOT_FOUND", errObj["code"])
	}
}

func TestAccountRemoveNotFoundJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "accounts", "remove", "999"})
	if code == 0 {
		t.Error("exit code should be non-zero for not-found account")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatal("error field missing in JSON output")
	}
	if errObj["code"] != "NOT_FOUND" {
		t.Errorf("error code = %v, want NOT_FOUND", errObj["code"])
	}
}

func TestAccountEnableNotFoundJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "accounts", "enable", "999"})
	if code == 0 {
		t.Error("exit code should be non-zero for not-found account")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatal("error field missing in JSON output")
	}
	if errObj["code"] != "NOT_FOUND" {
		t.Errorf("error code = %v, want NOT_FOUND", errObj["code"])
	}
}

func TestDoctorCommand(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"doctor"})
	// Doctor should exit 0 if no critical failures (warnings are OK).
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (no critical failures expected)", code)
	}
	if !strings.Contains(stdout, "database") {
		t.Errorf("stdout should contain database check, got %q", stdout)
	}
}

func TestDoctorCommandJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	stdout, _, code := executeCmd(t, []string{"--output", "json", "doctor"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		t.Fatal("data field missing")
	}
	checks, ok := data["checks"].([]any)
	if !ok {
		t.Fatal("checks field missing")
	}
	if len(checks) == 0 {
		t.Error("should have at least one check")
	}
}

func TestKeyAddNoDuplicateFullKeyInList(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	// Add a key.
	stdout1, _, code := executeCmd(t, []string{"--output", "json", "key", "add", "test1"})
	if code != 0 {
		t.Fatalf("key add failed: code %d", code)
	}
	var parsed1 map[string]any
	json.Unmarshal([]byte(stdout1), &parsed1)
	data1 := parsed1["data"].(map[string]any)
	fullKey := data1["full_key"].(string)

	// List keys — full key should NOT appear.
	stdout2, _, code := executeCmd(t, []string{"--output", "json", "key", "list"})
	if code != 0 {
		t.Fatalf("key list failed: code %d", code)
	}
	if strings.Contains(stdout2, fullKey) {
		t.Error("key list JSON should not contain the full key value")
	}
}

func TestKeyUpdateSchedulingJSON(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	// First add a key.
	stdout1, _, code := executeCmd(t, []string{"--output", "json", "key", "add", "test-sched"})
	if code != 0 {
		t.Fatalf("key add failed: code %d", code)
	}
	var parsed1 map[string]any
	json.Unmarshal([]byte(stdout1), &parsed1)
	data1 := parsed1["data"].(map[string]any)
	keyID := data1["id"].(float64)

	// Update scheduling.
	stdout2, _, code := executeCmd(t, []string{"--output", "json", "key", "update",
		strconv.FormatInt(int64(keyID), 10),
		"--scheduling-mode", "performance", "--no-sticky"})
	if code != 0 {
		t.Errorf("key update failed: code %d", code)
	}
	var parsed2 map[string]any
	json.Unmarshal([]byte(stdout2), &parsed2)
	data2 := parsed2["data"].(map[string]any)
	if data2["scheduling_mode"] != "performance" {
		t.Errorf("scheduling_mode = %v, want performance", data2["scheduling_mode"])
	}
}

func TestKeyUpdateInvalidSchedulingMode(t *testing.T) {
	cleanup := setupTempDB(t)
	defer cleanup()
	// First add a key.
	_, _, code := executeCmd(t, []string{"key", "add", "test"})
	if code != 0 {
		t.Fatalf("key add failed")
	}

	// Try invalid scheduling mode.
	_, stderr, code := executeCmd(t, []string{"key", "update", "1", "--scheduling-mode", "invalid"})
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (usage error for invalid scheduling mode)", code)
	}
	if stderr == "" {
		t.Error("stderr should contain error message")
	}
}
