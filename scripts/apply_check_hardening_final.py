from pathlib import Path
import re


def read(path: str) -> str:
    return Path(path).read_text(encoding="utf-8")


def write(path: str, content: str) -> None:
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(content, encoding="utf-8")


def patch_git_repository() -> None:
    path = "git_repository.go"
    text = read(path)

    start = text.find("func gitNetworkOutput(")
    if start >= 0:
        end = text.find("\nfunc ", start + 1)
        if end < 0:
            end = len(text)
        section = text[start:end]
        marker = 'cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)'
        if marker in section and "cmd.Env = gitNonInteractiveEnv()" not in section:
            section = section.replace(marker, marker + "\n\tcmd.Env = gitNonInteractiveEnv()", 1)
            text = text[:start] + section + text[end:]

    old = 'status, err := gitOutput(root, "status", "--porcelain")'
    if old in text:
        text = text.replace(old, "dirty, err := repositorySkillsDirty(root, items)", 1)
        process_start = text.find("func processRepository(")
        if process_start < 0:
            raise RuntimeError("processRepository not found")
        process_end = text.find("\nfunc ", process_start + 1)
        if process_end < 0:
            process_end = len(text)
        section = text[process_start:process_end]
        if 'status != ""' not in section:
            raise RuntimeError("repository dirty status check not found")
        section = section.replace('status != ""', "dirty", 1)
        text = text[:process_start] + section + text[process_end:]

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


def patch_skill_document() -> None:
    path = "skill_document.go"
    text = read(path)
    if "os.ReadFile(path)" in text:
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


def patch_provider_transactions() -> None:
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
            mkdir = re.search(
                r'(?m)^\t(?P<snapshot>[A-Za-z_]\w*), err := os\.MkdirTemp\((?P<parent>[A-Za-z_]\w*), "\.skillctl-provider-snapshot-"\)',
                text,
            )
            if not mkdir:
                raise RuntimeError(f"snapshot mkdir not found in {path}")
            snapshot = mkdir.group("snapshot")
            parent = mkdir.group("parent")
            declaration_matches = list(
                re.finditer(rf'(?m)^\t{re.escape(parent)} := [^\n]+\n', text[: mkdir.start()])
            )
            if not declaration_matches:
                raise RuntimeError(f"snapshot parent declaration not found in {path}")
            declaration = declaration_matches[-1]
            replacement = (
                f"\t{parent}, err := providerTransactionRoot()\n"
                "\tif err != nil {\n"
                "\t\treturn nil, err\n"
                "\t}\n"
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
                1,
            )
        write(path, text)


def patch_doctor() -> None:
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

func inspectSkillctlTransactions(fix bool, stdout io.Writer) {
\tcache, err := os.UserCacheDir()
\tif err != nil {
\t\treturn
\t}
\thome, _ := os.UserHomeDir()
\troots := []string{
\t\tfilepath.Join(cache, "skillctl", "transactions"),
\t\tfilepath.Join(cache, "skillctl", "sources"),
\t\tfilepath.Join(home, ".agents", "skills"),
\t\tfilepath.Join(home, ".codex", "skills"),
\t\tfilepath.Join(home, ".claude", "skills"),
\t\tfilepath.Join(home, ".cursor", "skills"),
\t}
\tfor _, root := range roots {
\t\tentries, readErr := os.ReadDir(root)
\t\tif readErr != nil {
\t\t\tcontinue
\t\t}
\t\tfor _, entry := range entries {
\t\t\tname := entry.Name()
\t\t\tisSnapshot := strings.HasPrefix(name, "provider-snapshot-") || strings.HasPrefix(name, ".skillctl-provider-snapshot-")
\t\t\tisSourceDebris := strings.Contains(name, ".corrupt-") || strings.HasPrefix(name, ".skillctl-source-clone-")
\t\t\tisBackup := strings.HasPrefix(name, ".skillctl-backup-")
\t\t\tisOtherTransaction := strings.HasPrefix(name, ".skillctl-stage-") || strings.HasPrefix(name, ".skillctl-restore-")
\t\t\tif !(isSnapshot || isSourceDebris || isBackup || isOtherTransaction) {
\t\t\t\tcontinue
\t\t\t}
\t\t\tpath := filepath.Join(root, name)
\t\t\tif isBackup {
\t\t\t\tfmt.Fprintf(stdout, "[warning] recoverable update backup requires review: %s\\n", path)
\t\t\t\tcontinue
\t\t\t}
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
    if "inspectSkillctlTransactions(fix, stdout)" not in text:
        inserted = False
        for name in ("runDoctor", "doctor"):
            match = re.search(rf'func {name}\((?P<params>[^)]*)\)[^{{]*\{{', text)
            if not match:
                continue
            params = match.group("params")
            if "fix bool" in params and "stdout io.Writer" in params:
                text = text[: match.end()] + "\n\tinspectSkillctlTransactions(fix, stdout)" + text[match.end() :]
                inserted = True
                break
        if not inserted:
            raise RuntimeError("doctor entrypoint with fix/stdout not found")
    write(path, text)


def patch_report_summary() -> None:
    path = "report.go"
    text = read(path)
    if "state := item.State" not in text:
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


def write_tests() -> None:
    write(
        "check_hardening_additional_test.go",
        '''package main

import (
\t"os"
\t"os/exec"
\t"path/filepath"
\t"strings"
\t"testing"
)

func TestCheckFrontMatterReaderDoesNotReadLargeBody(t *testing.T) {
\tpath := filepath.Join(t.TempDir(), "SKILL.md")
\tbody := strings.Repeat("body line\\n", 200000)
\tcontent := "---\\nname: large-body\\ndescription: bounded reader\\n---\\n" + body
\tif err := os.WriteFile(path, []byte(content), 0o644); err != nil {
\t\tt.Fatal(err)
\t}
\tdocument, err := readSkillDocument(path)
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif document.Name != "large-body" {
\t\tt.Fatalf("unexpected document: %#v", document)
\t}
}

func TestCheckRepositoryDirtyIsScopedToSkills(t *testing.T) {
\tif _, err := exec.LookPath("git"); err != nil {
\t\tt.Skip("git is not installed")
\t}
\troot := t.TempDir()
\tskillDir := filepath.Join(root, "skills", "demo")
\tif err := os.MkdirAll(skillDir, 0o755); err != nil {
\t\tt.Fatal(err)
\t}
\tif err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\\nname: demo\\ndescription: test\\n---\\n"), 0o644); err != nil {
\t\tt.Fatal(err)
\t}
\tif err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial\\n"), 0o644); err != nil {
\t\tt.Fatal(err)
\t}
\tfor _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "initial"}} {
\t\tcmd := exec.Command("git", args...)
\t\tcmd.Dir = root
\t\tif output, err := cmd.CombinedOutput(); err != nil {
\t\t\tt.Fatalf("git %v: %v: %s", args, err, output)
\t\t}
\t}
\tif err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\\n"), 0o644); err != nil {
\t\tt.Fatal(err)
\t}
\tdirty, err := repositorySkillsDirty(root, []skill{{Name: "demo", Path: skillDir}})
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif dirty {
\t\tt.Fatal("unrelated repository change marked the skill dirty")
\t}
\tif err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\\nname: demo\\ndescription: changed\\n---\\n"), 0o644); err != nil {
\t\tt.Fatal(err)
\t}
\tdirty, err = repositorySkillsDirty(root, []skill{{Name: "demo", Path: skillDir}})
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif !dirty {
\t\tt.Fatal("skill-local change was not detected")
\t}
}
''',
    )


patch_git_repository()
patch_skill_document()
patch_provider_transactions()
patch_doctor()
patch_report_summary()
write_tests()
