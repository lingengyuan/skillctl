//go:build ablation

package installhistory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkAblationHistory(b *testing.B) {
	root := b.TempDir()
	var content strings.Builder
	for i := range 4096 {
		fmt.Fprintf(&content, `{"type":"user","message":{"content":"skills %s"}}`+"\n", strings.Repeat("x", 4096))
		if i%256 == 0 {
			fmt.Fprintf(&content, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"npx skills add owner/repo --skill sample-%02d"}}]}}`+"\n", i/256)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), []byte(content.String()), 0600); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(content.Len()))
	b.ReportAllocs()
	for b.Loop() {
		candidates, err := ReadRoots([]string{root})
		if err != nil || len(candidates) != 16 {
			b.Fatalf("history candidates=%d: %v", len(candidates), err)
		}
		for i := range 16 {
			matches := candidates[fmt.Sprintf("sample-%02d", i)]
			if len(matches) != 1 || matches[0].Source != "https://github.com/owner/repo.git" {
				b.Fatal("history candidate identity changed")
			}
		}
	}
}
