package cli

import "testing"

func TestValidateDryRunAndTrackOptions(t *testing.T) {
	if err := Validate("list", Options{DryRun: true}); err == nil {
		t.Fatal("--dry-run was accepted for list")
	}
	if err := Validate("check", Options{Source: "repo"}); err == nil {
		t.Fatal("track-only source option was accepted for check")
	}
	if err := Validate("update", Options{DryRun: true}); err != nil {
		t.Fatalf("valid update --dry-run rejected: %v", err)
	}
}
