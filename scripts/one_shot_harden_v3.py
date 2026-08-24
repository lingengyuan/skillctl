from pathlib import Path
import re


def read(path: str) -> str:
    return Path(path).read_text(encoding="utf-8")


def write(path: str, content: str) -> None:
    Path(path).write_text(content, encoding="utf-8")


def patch_source_progress() -> None:
    path = "source_session.go"
    text = read(path)
    text = text.replace(
        's.progressf("Checking remote source %d/%d: %s%s...\\n", item.number, s.sourceCount, item.label, affectedSkillLabel(item.Skills))',
        's.progressf("Checking remote source %d... %s%s [%d/%d]\\n", item.number, item.label, affectedSkillLabel(item.Skills), item.number, s.sourceCount)',
    )
    text = text.replace(
        's.progressf("Remote source %d/%d failed: %s (%s).\\n", result.number, s.sourceCount, result.label, elapsed)',
        's.progressf("Remote source %d failed: %s [%d/%d] (%s).\\n", result.number, result.label, result.number, s.sourceCount, elapsed)',
    )
    text = text.replace(
        's.progressf("Remote source %d/%d ready: %s (%s).\\n", result.number, s.sourceCount, result.label, elapsed)',
        's.progressf("Remote source %d ready: %s [%d/%d] (%s).\\n", result.number, result.label, result.number, s.sourceCount, elapsed)',
    )
    text = text.replace(
        's.progressf("Checking remote source %d: %s...\\n", number, label)',
        's.progressf("Checking remote source %d... %s\\n", number, label)',
    )
    write(path, text)


def patch_report_compatibility() -> None:
    path = "report.go"
    text = read(path)
    needle = '''\tif len(failureStages) == 1 {
\t\tfor value := range failureStages {
\t\t\tmerged.FailureStage = value
\t\t}
\t} else {
\t\tmerged.FailureStage = ""
\t}
\treturn merged
}'''
    replacement = '''\tif len(failureStages) == 1 {
\t\tfor value := range failureStages {
\t\t\tmerged.FailureStage = value
\t\t}
\t} else {
\t\tmerged.FailureStage = ""
\t}
\tif !reportNeedsInstallationDetails(merged) {
\t\tmerged.Path = group[0].Path
\t\tmerged.ScanRoot = group[0].ScanRoot
\t}
\treturn merged
}'''
    if needle in text:
        text = text.replace(needle, replacement, 1)
    write(path, text)


def patch_doctor_legacy_transactions() -> None:
    path = "doctor_transactions.go"
    if not Path(path).exists():
        return
    text = read(path)
    text = text.replace(
        '''\troots := []string{
\t\tfilepath.Join(cache, "skillctl", "transactions"),
\t\tfilepath.Join(cache, "skillctl", "sources"),
\t}''',
        '''\thome, _ := os.UserHomeDir()
\troots := []string{
\t\tfilepath.Join(cache, "skillctl", "transactions"),
\t\tfilepath.Join(cache, "skillctl", "sources"),
\t\tfilepath.Join(home, ".agents", "skills"),
\t\tfilepath.Join(home, ".codex", "skills"),
\t\tfilepath.Join(home, ".claude", "skills"),
\t\tfilepath.Join(home, ".cursor", "skills"),
\t}''',
    )
    text = text.replace(
        '''\t\t\tif !(strings.HasPrefix(name, "provider-snapshot-") || strings.Contains(name, ".corrupt-") || strings.HasPrefix(name, ".skillctl-source-clone-")) {
\t\t\t\tcontinue
\t\t\t}''',
        '''\t\t\tisProviderSnapshot := strings.HasPrefix(name, "provider-snapshot-") || strings.HasPrefix(name, ".skillctl-provider-snapshot-")
\t\t\tisSourceDebris := strings.Contains(name, ".corrupt-") || strings.HasPrefix(name, ".skillctl-source-clone-")
\t\t\tisRecoverableBackup := strings.HasPrefix(name, ".skillctl-backup-")
\t\t\tisOtherTransaction := strings.HasPrefix(name, ".skillctl-stage-") || strings.HasPrefix(name, ".skillctl-restore-")
\t\t\tif !(isProviderSnapshot || isSourceDebris || isRecoverableBackup || isOtherTransaction) {
\t\t\t\tcontinue
\t\t\t}
\t\t\tif isRecoverableBackup {
\t\t\t\tfmt.Fprintf(stdout, "[warning] recoverable update backup requires review: %s\\n", filepath.Join(root, name))
\t\t\t\tcontinue
\t\t\t}''',
    )
    write(path, text)


def patch_regression_test() -> None:
    path = "reliability_test.go"
    if not Path(path).exists():
        return
    text = read(path)
    text = text.replace(
        r'`Remote source 5/5 ready: .* \((\d+)ms\)`',
        r'`Remote source 5 ready: .*\[5/5\] \((\d+)ms\)`',
    )
    write(path, text)


def patch_source_cache_comment() -> None:
    path = "source_cache.go"
    text = read(path)
    text = text.replace(
        "// syncObjectSource maintains a bare repository for provider checks. It never\n// checks out the repository, so a large source does not pay worktree creation\n// cost merely to compare one or more skill trees.",
        "// syncObjectSource maintains an object cache without populating its worktree.\n// A large source does not pay checkout cost merely to compare one or more skill trees.",
    )
    write(path, text)


patch_source_progress()
patch_report_compatibility()
patch_doctor_legacy_transactions()
patch_regression_test()
patch_source_cache_comment()
