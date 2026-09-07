package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransactionRecoversAfterProcessExit(t *testing.T) {
	if os.Getenv("SKILLCTL_TEST_CRASH_CHILD") == "1" {
		target := os.Getenv("SKILLCTL_TEST_CRASH_TARGET")
		change, err := mutation(target, "file")
		if err != nil {
			os.Exit(21)
		}
		change.Data = []byte("new")
		record, err := prepareOperation("crash-test", []pathMutation{change}, nil)
		if err != nil {
			os.Exit(22)
		}
		record.State = "applying"
		if record.save() != nil {
			os.Exit(23)
		}
		if record.applyStep(0) != nil {
			os.Exit(24)
		}
		os.Exit(0)
	}
	root := t.TempDir()
	t.Setenv("SKILLCTL_HOME", filepath.Join(root, "state"))
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTransactionRecoversAfterProcessExit$")
	cmd.Env = append(os.Environ(), "SKILLCTL_TEST_CRASH_CHILD=1", "SKILLCTL_TEST_CRASH_TARGET="+target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Fatalf("child never applied: %s", data)
	}
	lock, err := acquireCommandLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	if err := recoverOperations(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "old" {
		t.Fatalf("recovery lost original: %s", data)
	}
	history, err := readOperations()
	if err != nil || len(history) != 1 || history[0].State != "rolled_back" {
		t.Fatalf("history: %#v %v", history, err)
	}
}

func TestTransactionPartialFailurePreservesNewUserChanges(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SKILLCTL_HOME", filepath.Join(root, "state"))
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := mutation(first, "file")
	a.Data = []byte("updated")
	b, _ := mutation(second, "file")
	b.Data = []byte("updated")
	op, err := prepareOperation("fault", []pathMutation{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("user work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := op.apply(); err == nil {
		t.Fatal("precondition change accepted")
	}
	if data, _ := os.ReadFile(first); string(data) != "old" {
		t.Fatalf("first step not rolled back: %s", data)
	}
	if data, _ := os.ReadFile(second); string(data) != "user work" {
		t.Fatalf("new user edits lost: %s", data)
	}
	if op.State != "rolled_back" {
		t.Fatalf("wrong state: %s", op.State)
	}
}

func TestRecoveryFailureRetainsEvidence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SKILLCTL_HOME", filepath.Join(root, "state"))
	path := filepath.Join(root, "target")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	change, _ := mutation(path, "file")
	change.Data = []byte("new")
	op, err := prepareOperation("fault", []pathMutation{change}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.applyStep(0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new user changes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := op.restore(); err == nil {
		t.Fatal("recovery overwrote new user changes")
	}
	if op.State != "recovery_required" {
		t.Fatalf("wrong recovery state: %s", op.State)
	}
	if data, _ := os.ReadFile(op.imagePath("before", 0)); string(data) != "old" {
		t.Fatal("before image lost")
	}
	if data, _ := os.ReadFile(path); string(data) != "new user changes" {
		t.Fatal("user changes lost")
	}
}

func TestRollbackRejectsChangedOrTamperedAfterState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SKILLCTL_HOME", filepath.Join(root, "state"))
	path := filepath.Join(root, "target")
	change, _ := mutation(path, "file")
	change.Data = []byte("installed")
	op, err := prepareOperation("install", []pathMutation{change}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.apply(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackMutations(op.ID); err == nil {
		t.Fatal("rollback accepted local modifications")
	}
}

func TestOperationRejectsJournalInsideTarget(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SKILLCTL_HOME", filepath.Join(root, "state"))
	change, _ := mutation(root, "remove")
	_, err := prepareOperation("remove", []pathMutation{change}, nil)
	if err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("unsafe recursive backup accepted: %v", err)
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		t.Fatal("target removed")
	}
}
