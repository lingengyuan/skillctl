from pathlib import Path
import re


def read(path: str) -> str:
    return Path(path).read_text(encoding="utf-8")


def write(path: str, content: str) -> None:
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(content, encoding="utf-8")


def replace_function(path: str, name: str, replacement: str) -> None:
    text = read(path)
    pattern = re.compile(rf"(?ms)^func {re.escape(name)}\(.*?(?=^func |^type |\Z)")
    match = pattern.search(text)
    if not match:
        raise RuntimeError(f"function {name} not found in {path}")
    current = match.group(0).strip()
    if current == replacement.strip():
        return
    write(path, text[: match.start()] + replacement.rstrip() + "\n\n" + text[match.end() :])


def ensure_tracked_worktrees() -> None:
    path = "tracked.go"
    text = read(path)
    text = text.replace(
        "cache, err := session.source(entry.Source, entry.Ref)",
        "cache, err := session.worktreeSource(entry.Source, entry.Ref)",
    )
    write(path, text)
    replace_function(
        path,
        "syncSource",
        '''func syncSource(ctx context.Context, source, ref string) (string, error) {
\treturn syncWorktreeSource(ctx, source, ref)
}''',
    )
    replace_function(
        path,
        "sourceSkillPath",
        '''func sourceSkillPath(cache, path string) (string, error) {
\tif path == "" {
\t\treturn "", fmt.Errorf("tracked source path is empty")
\t}
\tclean := filepath.Clean(filepath.FromSlash(path))
\tif clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
\t\treturn "", fmt.Errorf("tracked source path %q is invalid", path)
\t}
\t// Provider checks keep object caches unmaterialized. A provider update
\t// pays checkout cost only when it actually needs source files.
\tif _, err := gitOutput(cache, "rev-parse", "--verify", "refs/skillctl/selected^{commit}"); err == nil {
\t\tif _, err := gitOutput(cache, "checkout", "--force", "--detach", "refs/skillctl/selected"); err != nil {
\t\t\treturn "", fmt.Errorf("materialize provider source: %w", err)
\t\t}
\t}
\ttarget := filepath.Join(cache, clean)
\tif !within(cache, target) {
\t\treturn "", fmt.Errorf("tracked source path %q escapes source root", path)
\t}
\tinfo, err := os.Stat(target)
\tif err != nil {
\t\treturn "", fmt.Errorf("tracked source path %q: %w", path, err)
\t}
\tif !info.IsDir() {
\t\treturn "", fmt.Errorf("tracked source path %q is not a directory", path)
\t}
\treturn target, nil
}''',
    )
    text = read(path)
    if "exec." not in text:
        text = text.replace('\t"os/exec"\n', "")
    if "time." not in text:
        text = text.replace('\t"time"\n', "")
    write(path, text)


def ensure_object_cache() -> None:
    path = "source_cache.go"
    text = read(path)
    text = text.replace(
        '''\tif kind == "object" {
\t\tname += ".git"
\t}
''',
        "",
    )
    text = re.sub(
        r"(?ms)^func validObjectCache\(cache string\) bool \{.*?^\}",
        '''func validObjectCache(cache string) bool {
\treturn validWorktreeCache(cache)
}''',
        text,
        count=1,
    )
    text = text.replace(
        "cloneSourceAtomically(ctx, source, cache, true)",
        "cloneSourceAtomically(ctx, source, cache, false)",
    )
    text = text.replace(
        'gitNetworkOutput(ctx, cache, "fetch", "--prune", "--force", "--recurse-submodules=no", "origin", "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")',
        'gitNetworkOutput(ctx, cache, "fetch", "--prune", "--force", "--recurse-submodules=no", "origin")',
    )
    sync_start = text.find("func syncObjectSource(")
    if sync_start < 0:
        raise RuntimeError("syncObjectSource not found")
    sync_end = text.find("func resolveObjectRevision(", sync_start)
    if sync_end < 0:
        raise RuntimeError("resolveObjectRevision not found")
    prefix = text[:sync_start]
    body = text[sync_start:sync_end].replace(
        "resolveObjectRevision(cache, ref)", "resolveSourceRevision(cache, ref)"
    )
    write(path, prefix + body + text[sync_end:])


def ensure_git_hardening() -> None:
    path = "git_repository.go"
    text = read(path)
    marker = 'cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)'
    if marker in text and marker + "\n\tcmd.Env = gitNonInteractiveEnv()" not in text:
        text = text.replace(marker, marker + "\n\tcmd.Env = gitNonInteractiveEnv()", 1)
    text = text.replace(
        'status, err := gitOutput(root, "status", "--porcelain")',
        "dirty, err := repositorySkillsDirty(root, items)",
        1,
    )
    process_start = text.find("func processRepository(")
    process_end = text.find("\nfunc ", process_start + 1)
    if process_start >= 0:
        if process_end < 0:
            process_end = len(text)
        segment = text[process_start:process_end].replace('status != ""', "dirty", 1)
        text = text[:process_start] + segment + text[process_end:]
    write(path, text)
    write(
        "git_dirty.go",
        '''package main

import (
\t"fmt"
\t"path/filepath"
)

func repositorySkillsDirty(root string, items []skill) (bool, error) {
\tfor _, item := range items {
\t\trelative, err := filepath.Rel(root, item.Path)
\t\tif err != nil {
\t\t\treturn false, fmt.Errorf("resolve skill path %s: %w", item.Path, err)
\t\t}
\t\tstatus, err := gitOutput(root, "status", "--porcelain", "--", relative)
\t\tif err != nil {
\t\t\treturn false, err
\t\t}
\t\tif status != "" {
\t\t\treturn true, nil
\t\t}
\t}
\treturn false, nil
}
''',
    )


def ensure_bounded_frontmatter() -> None:
    path = "skill_document.go"
    text = read(path)
    text = text.replace("os.ReadFile(path)", "readSkillFrontMatter(path)", 1)
    if "os." not in text:
        text = text.replace('\t"os"\n', "")
    write(path, text)
    write(
        "skill_document_reader.go",
        '''package main

import (
\t"bufio"
\t"bytes"
\t"fmt"
\t"io"
\t"os"
\t"strings"
)

const maxSkillFrontMatterBytes = 256 * 1024

func readSkillFrontMatter(path string) ([]byte, error) {
\tfile, err := os.Open(path)
\tif err != nil {
\t\treturn nil, err
\t}
\tdefer file.Close()

\treader := bufio.NewReader(file)
\tvar content bytes.Buffer
\ttotal := 0
\tlineNumber := 0
\tfor {
\t\tline, readErr := reader.ReadString('\\n')
\t\tlineNumber++
\t\ttotal += len(line)
\t\tif total > maxSkillFrontMatterBytes {
\t\t\treturn nil, fmt.Errorf("front matter exceeds %d bytes", maxSkillFrontMatterBytes)
\t\t}
\t\tcontent.WriteString(line)
\t\ttrimmed := strings.TrimSpace(line)
\t\tif lineNumber == 1 && trimmed != "---" {
\t\t\treturn content.Bytes(), nil
\t\t}
\t\tif lineNumber > 1 && trimmed == "---" {
\t\t\treturn content.Bytes(), nil
\t\t}
\t\tif readErr != nil {
\t\t\tif readErr == io.EOF {
\t\t\t\treturn nil, fmt.Errorf("unterminated front matter")
\t\t\t}
\t\t\treturn nil, readErr
\t\t}
\t}
}
''',
    )


def ensure_provider_transactions() -> None:
    write(
        "provider_transactions.go",
        '''package main

import (
\t"fmt"
\t"os"
\t"path/filepath"
)

func providerTransactionRoot() (string, error) {
\tcache, err := os.UserCacheDir()
\tif err != nil {
\t\treturn "", fmt.Errorf("find user cache directory: %w", err)
\t}
\troot := filepath.Join(cache, "skillctl", "transactions")
\tif err := os.MkdirAll(root, 0o700); err != nil {
\t\treturn "", fmt.Errorf("create provider transaction directory: %w", err)
\t}
\treturn root, nil
}
''',
    )
    for path in ("vercel_provider.go", "gh_provider.go"):
        text = read(path)
        if '".skillctl-provider-snapshot-"' in text:
            match = re.search(
                r'(?m)^\t(?P<snapshot>[A-Za-z_]\w*), err := os\.MkdirTemp\((?P<parent>[A-Za-z_]\w*), "\.skillctl-provider-snapshot-"\)',
                text,
            )
            if not match:
                raise RuntimeError(f"snapshot mkdir not found in {path}")
            parent = match.group("parent")
            snapshot = match.group("snapshot")
            declaration = re.search(
                rf'(?m)^\t{re.escape(parent)} := [^\n]+\n', text[: match.start()]
            )
            if not declaration:
                raise RuntimeError(f"snapshot parent declaration not found in {path}")
            replacement = (
                f"\t{parent}, err := providerTransactionRoot()\n"
                "\tif err != nil {\n\t\treturn nil, err\n\t}\n"
            )
            text = text[: declaration.start()] + replacement + text[declaration.end() :]
            text = text.replace(
                f'{snapshot}, err := os.MkdirTemp({parent}, ".skillctl-provider-snapshot-")',
                f'{snapshot}, err := os.MkdirTemp({parent}, "provider-snapshot-")',
                1,
            )
        text = re.sub(
            r'(?ms)^func \(s \*(?P<kind>[A-Za-z_]\w*)\) cleanup\(\) \{\s*_ = os\.RemoveAll\(s\.directory\)\s*\}',
            lambda match: f'func (s *{match.group("kind")}) cleanup() error {{\n\treturn os.RemoveAll(s.directory)\n}}',
            text,
            count=1,
        )
        if "defer snapshot.cleanup()" in text:
            text = text.replace(
                "defer snapshot.cleanup()",
                '''defer func() {
\t\tif cleanupErr := snapshot.cleanup(); cleanupErr != nil && progress != nil {
\t\t\tfmt.Fprintf(progress, "Warning: remove provider snapshot: %v\\n", cleanupErr)
\t\t}
\t}()''',
            )
        write(path, text)


def ensure_report_summary() -> None:
    path = "report.go"
    text = read(path)
    text = text.replace(
        "\t\tswitch item.State {",
        "\t\tstate := item.State\n\t\tif state == \"\" {\n\t\t\tstate, _ = classifyReport(item)\n\t\t}\n\t\tswitch state {",
        1,
    )
    start = text.find("func printReports(")
    if start < 0:
        raise RuntimeError("printReports not found")
    brace = text.find("{", start)
    depth = 0
    end = None
    for index in range(brace, len(text)):
        if text[index] == "{":
            depth += 1
        elif text[index] == "}":
            depth -= 1
            if depth == 0:
                end = index
                break
    if end is None:
        raise RuntimeError("printReports closing brace not found")
    if "printReportSummary(w, reports)" not in text[brace:end]:
        text = text[:end] + "\tprintReportSummary(w, reports)\n" + text[end:]
    write(path, text)


def ensure_doctor_transactions() -> None:
    write(
        "doctor_transactions.go",
        '''package main

import (
\t"fmt"
\t"io"
\t"os"
\t"path/filepath"
\t"strings"
)

func inspectProviderTransactions(fix bool, stdout io.Writer) {
\tcache, err := os.UserCacheDir()
\tif err != nil {
\t\treturn
\t}
\troots := []string{
\t\tfilepath.Join(cache, "skillctl", "transactions"),
\t\tfilepath.Join(cache, "skillctl", "sources"),
\t}
\tfor _, root := range roots {
\t\tentries, readErr := os.ReadDir(root)
\t\tif readErr != nil {
\t\t\tcontinue
\t\t}
\t\tfor _, entry := range entries {
\t\t\tname := entry.Name()
\t\t\tif !(strings.HasPrefix(name, "provider-snapshot-") || strings.Contains(name, ".corrupt-") || strings.HasPrefix(name, ".skillctl-source-clone-")) {
\t\t\t\tcontinue
\t\t\t}
\t\t\tpath := filepath.Join(root, name)
\t\t\tif fix {
\t\t\t\tif removeErr := os.RemoveAll(path); removeErr != nil {
\t\t\t\t\tfmt.Fprintf(stdout, "[warning] could not remove orphan transaction %s: %v\\n", path, removeErr)
\t\t\t\t} else {
\t\t\t\t\tfmt.Fprintf(stdout, "[fixed] removed orphan transaction %s\\n", path)
\t\t\t\t}
\t\t\t} else {
\t\t\t\tfmt.Fprintf(stdout, "[warning] orphan transaction/cache entry: %s\\n", path)
\t\t\t}
\t\t}
\t}
}
''',
    )
    path = "doctor.go"
    text = read(path)
    if "inspectProviderTransactions(fix, stdout)" in text:
        return
    for name in ("runDoctor", "doctor"):
        match = re.search(rf'func {name}\((?P<params>[^)]*)\)[^{{]*\{{', text)
        if not match:
            continue
        params = match.group("params")
        if "fix bool" in params and "stdout io.Writer" in params:
            text = text[: match.end()] + "\n\tinspectProviderTransactions(fix, stdout)" + text[match.end() :]
            write(path, text)
            return


def ensure_tests() -> None:
    write(
        "reliability_test.go",
        '''package main

import (
\t"bytes"
\t"context"
\t"fmt"
\t"os"
\t"os/exec"
\t"path/filepath"
\t"regexp"
\t"strconv"
\t"strings"
\t"sync/atomic"
\t"testing"
\t"time"
)

func writeReliabilitySkill(t *testing.T, dir, name string) {
\tt.Helper()
\tif err := os.MkdirAll(dir, 0o755); err != nil {
\t\tt.Fatal(err)
\t}
\tcontent := fmt.Sprintf("---\\nname: %s\\ndescription: reliability test\\n---\\n", name)
\tif err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
\t\tt.Fatal(err)
\t}
}

func TestScanIgnoresTransactionDirectories(t *testing.T) {
\troot := t.TempDir()
\twriteReliabilitySkill(t, filepath.Join(root, "real"), "real-skill")
\twriteReliabilitySkill(t, filepath.Join(root, ".skillctl-provider-snapshot-123"), "snapshot-skill")
\twriteReliabilitySkill(t, filepath.Join(root, ".skillctl-stage-123"), "stage-skill")
\tvar stderr bytes.Buffer
\titems, failed := scan([]scanRoot{{Path: root, Host: "test", Scope: "user", Required: true}}, false, &stderr)
\tif failed || len(items) != 1 || items[0].Name != "real-skill" {
\t\tt.Fatalf("unexpected scan result: failed=%v items=%#v stderr=%s", failed, items, stderr.String())
\t}
}

func TestScanStopsInsideValidSkillRoot(t *testing.T) {
\troot := t.TempDir()
\touter := filepath.Join(root, "outer")
\twriteReliabilitySkill(t, outer, "outer-skill")
\twriteReliabilitySkill(t, filepath.Join(outer, "references", "nested"), "nested-skill")
\tvar stderr bytes.Buffer
\titems, failed := scan([]scanRoot{{Path: root, Host: "test", Scope: "user", Required: true}}, false, &stderr)
\tif failed || len(items) != 1 || items[0].Name != "outer-skill" {
\t\tt.Fatalf("unexpected scan result: failed=%v items=%#v stderr=%s", failed, items, stderr.String())
\t}
}

func TestSourceDisplayLabelRemovesCredentials(t *testing.T) {
\tlabel := sourceDisplayLabel("https://user:secret@example.com/acme/skills.git?token=hidden#fragment", "main")
\tif label != "example.com/acme/skills@main" || strings.Contains(label, "secret") || strings.Contains(label, "hidden") {
\t\tt.Fatalf("unexpected or unsafe label %q", label)
\t}
}

func TestSourceElapsedExcludesWorkerQueueTime(t *testing.T) {
\toriginal := syncSourceForSession
\tdefer func() { syncSourceForSession = original }()
\tgate := make(chan struct{})
\tvar calls atomic.Int32
\tsyncSourceForSession = func(ctx context.Context, source, ref string) (string, error) {
\t\tn := calls.Add(1)
\t\tif n <= maxConcurrentSourceChecks {
\t\t\tselect {
\t\t\tcase <-gate:
\t\t\tcase <-ctx.Done():
\t\t\t\treturn "", ctx.Err()
\t\t\t}
\t\t} else {
\t\t\ttime.Sleep(5 * time.Millisecond)
\t\t}
\t\treturn source, nil
\t}
\tgo func() {
\t\ttime.Sleep(80 * time.Millisecond)
\t\tclose(gate)
\t}()
\tvar progress bytes.Buffer
\tsession := newSourceSession(context.Background(), time.Second, &progress)
\trequests := make([]sourceRequest, 5)
\tfor i := range requests {
\t\trequests[i] = sourceRequest{Source: fmt.Sprintf("https://example.com/source-%d.git", i+1), Skills: []string{fmt.Sprintf("skill-%d", i+1)}}
\t}
\tsession.prefetch(requests)
\tmatch := regexp.MustCompile(`Remote source 5/5 ready: .* \\((\\d+)ms\\)`).FindStringSubmatch(progress.String())
\tif len(match) != 2 {
\t\tt.Fatalf("source 5 timing missing:\\n%s", progress.String())
\t}
\tmillis, _ := strconv.Atoi(match[1])
\tif millis >= 50 {
\t\tt.Fatalf("queue delay leaked into execution time: %dms\\n%s", millis, progress.String())
\t}
}

func TestMergedReportDoesNotBorrowAnotherInstallationPath(t *testing.T) {
\tfirst := reportFor(skill{Name: "shared", Path: "/first"}, "provider-a", "owner-a", nil, "clean", "up to date", false, "report-only", "")
\tsecond := reportFor(skill{Name: "shared", Path: "/second"}, "provider-b", "owner-b", nil, "unknown", "provider check failed", false, "report-only", "network timeout")
\tmerged := mergeReportsByIdentity([]report{first, second})
\tif len(merged) != 1 || merged[0].Path != "" || len(merged[0].Installations) != 2 {
\t\tt.Fatalf("merged report is misleading: %#v", merged)
\t}
}

func TestSkillDocumentReaderIgnoresLargeBody(t *testing.T) {
\tpath := filepath.Join(t.TempDir(), "SKILL.md")
\tbody := strings.Repeat("body line\\n", 200000)
\tcontent := "---\\nname: large-body\\ndescription: bounded reader\\n---\\n" + body
\tif err := os.WriteFile(path, []byte(content), 0o644); err != nil {
\t\tt.Fatal(err)
\t}
\tdocument, err := readSkillDocument(path)
\tif err != nil || document.Name != "large-body" {
\t\tt.Fatalf("unexpected document=%#v err=%v", document, err)
\t}
}

func TestObjectSourceCacheDefersCheckout(t *testing.T) {
\tif _, err := exec.LookPath("git"); err != nil {
\t\tt.Skip("git not installed")
\t}
\thome := t.TempDir()
\tt.Setenv("HOME", home)
\tt.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
\tt.Setenv("LOCALAPPDATA", filepath.Join(home, "cache"))
\tsource := filepath.Join(home, "source")
\twriteReliabilitySkill(t, filepath.Join(source, "demo"), "demo")
\tfor _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "initial"}} {
\t\tcmd := exec.Command("git", args...)
\t\tcmd.Dir = source
\t\tif output, err := cmd.CombinedOutput(); err != nil {
\t\t\tt.Fatalf("git %v: %v: %s", args, err, output)
\t\t}
\t}
\tctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
\tdefer cancel()
\tcache, err := syncObjectSource(ctx, source, "")
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif _, err := os.Stat(filepath.Join(cache, "demo", "SKILL.md")); !os.IsNotExist(err) {
\t\tt.Fatalf("object cache unexpectedly checked out files: %v", err)
\t}
\tpath, err := sourceSkillPath(cache, "demo")
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
\t\tt.Fatalf("lazy checkout missing skill: %v", err)
\t}
}
''',
    )


ensure_tracked_worktrees()
ensure_object_cache()
ensure_git_hardening()
ensure_bounded_frontmatter()
ensure_provider_transactions()
ensure_report_summary()
ensure_doctor_transactions()
ensure_tests()
