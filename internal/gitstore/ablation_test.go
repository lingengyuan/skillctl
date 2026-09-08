//go:build ablation

package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The runner flips this expectation only in the isolated legacy-lock variant.
const ablationLegacyLock = false

func BenchmarkAblationExitedLock(b *testing.B) {
	child := exec.Command(os.Args[0], "-test.run=^$")
	if out, err := child.CombinedOutput(); err != nil {
		b.Fatalf("child: %v %s", err, out)
	}
	marker := []byte(fmt.Sprintf("pid=%d\ncreated=ablation\n", child.Process.Pid))
	cache := filepath.Join(b.TempDir(), "source")
	timeouts := 0
	b.ReportAllocs()
	for b.Loop() {
		if err := os.WriteFile(cache+".lock", marker, 0600); err != nil {
			b.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(b.Context(), 50*time.Millisecond)
		lock, err := acquireSourceCacheLock(ctx, cache)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			timeouts++
		} else if err != nil {
			b.Fatal(err)
		}
		lock.release()
		if ablationLegacyLock != errors.Is(err, context.DeadlineExceeded) {
			b.Fatalf("unexpected lock recovery result: %v", err)
		}
	}
	b.ReportMetric(float64(timeouts)/float64(b.N), "timeouts/op")
}
