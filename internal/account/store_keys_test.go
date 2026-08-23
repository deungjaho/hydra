package account

import (
	"testing"
)

func TestUpdateAPIKeyScheduling(t *testing.T) {
	d := testDB(t)
	id, err := AddAPIKey(d, "test-key-sched", "sched-test")
	if err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}

	// Set per-key scheduling override.
	if err := UpdateAPIKeyScheduling(d, id, "balance", true); err != nil {
		t.Fatalf("UpdateAPIKeyScheduling: %v", err)
	}

	k, err := GetAPIKey(d, id)
	if err != nil {
		t.Fatalf("GetAPIKey: %v", err)
	}
	if k.SchedulingMode != "balance" {
		t.Errorf("SchedulingMode = %q, want balance", k.SchedulingMode)
	}
	if !k.NoSticky {
		t.Errorf("NoSticky = false, want true")
	}

	// Clear scheduling override.
	if err := UpdateAPIKeyScheduling(d, id, "", false); err != nil {
		t.Fatalf("UpdateAPIKeyScheduling (clear): %v", err)
	}
	k, _ = GetAPIKey(d, id)
	if k.SchedulingMode != "" {
		t.Errorf("SchedulingMode = %q, want empty", k.SchedulingMode)
	}
	if k.NoSticky {
		t.Errorf("NoSticky = true, want false")
	}
}

func TestFindAPIKeyIncludesSchedulingFields(t *testing.T) {
	d := testDB(t)
	_, err := AddAPIKey(d, "test-key-find", "find-test")
	if err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}
	id, _ := GetAPIKey(d, 1)
	_ = UpdateAPIKeyScheduling(d, id.ID, "performance", true)

	k, err := FindAPIKey(d, "test-key-find")
	if err != nil {
		t.Fatalf("FindAPIKey: %v", err)
	}
	if k == nil {
		t.Fatal("FindAPIKey returned nil")
	}
	if k.SchedulingMode != "performance" {
		t.Errorf("SchedulingMode = %q, want performance", k.SchedulingMode)
	}
	if !k.NoSticky {
		t.Errorf("NoSticky = false, want true")
	}
}

func TestDisabledKeyNotFound(t *testing.T) {
	d := testDB(t)
	id, _ := AddAPIKey(d, "test-key-disabled", "disabled-test")
	_ = SetAPIKeyDisabled(d, id, true)

	k, err := FindAPIKey(d, "test-key-disabled")
	if err != nil {
		t.Fatalf("FindAPIKey error: %v", err)
	}
	if k != nil {
		t.Errorf("FindAPIKey on disabled key should return nil, got %v", k)
	}
}
