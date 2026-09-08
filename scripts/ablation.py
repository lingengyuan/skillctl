#!/usr/bin/env python3
"""Measure isolated optimization removals without changing the working tree.

Only the Python standard library and the project's Go toolchain are required.
All source adapters are local fixtures; no installed Skills are read or updated.
"""

import argparse
import csv
import hashlib
import json
import os
from pathlib import Path
import platform
import random
import shutil
import statistics
import subprocess
import tempfile
import time


ROOT = Path(__file__).resolve().parents[1]
FACTORS = {"buffer_reuse", "source_index", "dedup", "parallel", "kernel_lock"}
VARIANTS = {
    "full": set(),
    "no_buffer_reuse": {"buffer_reuse"},
    "no_source_index": {"source_index"},
    "no_dedup": {"dedup"},
    "serial": {"parallel"},
    "legacy_lock": {"kernel_lock"},
    "no_dedup_serial": {"dedup", "parallel"},
    "all_off": FACTORS,
    "with_history_filter": set(),
    "with_history_filter_no_buffer": {"buffer_reuse"},
}
PACKAGES = ("app", "installhistory", "gitstore")
BENCHMARKS = {
    "BenchmarkAblationHistory",
    "BenchmarkAblationRecoveryFailed",
    "BenchmarkAblationRecoveryVerified",
    "BenchmarkAblationExitedLock",
}

# The v0.0.3 marker-lock policy, kept only as an experiment fixture. The test
# starts with a real exited child's PID and gives acquisition a 50 ms budget.
LEGACY_LOCK = '''func acquireSourceCacheLock(ctx context.Context, cache string) (*sourceCacheLock, error) {
    lockPath := cache + ".lock"
    if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil { return nil, err }
    for {
        file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
        if err == nil {
            _, _ = fmt.Fprintf(file, "pid=%d\\ncreated=%s\\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
            if err := file.Close(); err != nil { _ = os.Remove(lockPath); return nil, err }
            return &sourceCacheLock{legacyPath: lockPath}, nil
        }
        if !errors.Is(err, os.ErrExist) { return nil, err }
        if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) > 30*time.Minute {
            if err := os.Remove(lockPath); err == nil || errors.Is(err, os.ErrNotExist) { continue }
        }
        select {
        case <-ctx.Done(): return nil, fmt.Errorf("wait for source cache lock: %w", ctx.Err())
        case <-time.After(50*time.Millisecond):
        }
    }
}
'''


def replace_once(path, old, new):
    text = path.read_text()
    if text.count(old) != 1:
        raise RuntimeError(f"Ablation patch no longer matches exactly once: {path.name}: {old[:70]!r}")
    path.write_text(text.replace(old, new, 1))


def apply_variant(path, disabled, variant):
    history = path / "internal/installhistory/history.go"
    recovery = path / "internal/app/install_history.go"
    session = path / "internal/app/source_session.go"
    if variant.startswith("with_history_filter"):
        # Rejected candidate retained only in temporary experimental copies.
        replace_once(history, "func trustedCommands(line []byte) []string {", '''func trustedCommands(line []byte) []string {
    if !bytes.Contains(line, []byte(`"exec_command"`)) &&
       !bytes.Contains(line, []byte(`"exec"`)) &&
       !bytes.Contains(line, []byte(`"Bash"`)) &&
       !bytes.Contains(line, []byte(`"bash"`)) &&
       !bytes.Contains(line, []byte(`\\u`)) { return nil }
''')
    if "buffer_reuse" in disabled:
        replace_once(history, "\tvar fragments []byte\n", "")
        start = history.read_text().index("\t\tline, readErr := reader.ReadSlice('\\n')")
        end = history.read_text().index("\n\t\tif !bytes.Contains(line", start)
        text = history.read_text()
        history.write_text(text[:start] + "\t\tline, readErr := reader.ReadBytes('\\n')" + text[end:])
    if "source_index" in disabled:
        replace_once(recovery, "if !cached {", "if !cached || true {")
    if "dedup" in disabled:
        replace_once(session, "if index, found := byKey[key]; found {", "if index, found := byKey[key]; found && false {")
    if "parallel" in disabled:
        replace_once(session, "const maxConcurrentSourceChecks = 4", "const maxConcurrentSourceChecks = 1")
    if "kernel_lock" in disabled:
        cache = path / "internal/gitstore/cache.go"
        replace_once(cache, "type sourceCacheLock struct {", "type sourceCacheLock struct {\nlegacyPath string")
        replace_once(cache, "func (l *sourceCacheLock) release() {", "func (l *sourceCacheLock) release() {\nif l != nil && l.legacyPath != \"\" { _ = os.Remove(l.legacyPath); return }")
        text = cache.read_text()
        start = text.index("func acquireSourceCacheLock(")
        end = text.index("\n// Older binaries", start)
        cache.write_text(text[:start] + LEGACY_LOCK + text[end:])
        replace_once(path / "internal/gitstore/ablation_test.go", "const ablationLegacyLock = false", "const ablationLegacyLock = true")


def run(command, cwd, env):
    result = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True, timeout=180)
    if result.returncode:
        raise RuntimeError(f"Command failed: {command}\n{result.stdout}\n{result.stderr}")
    return result.stdout


def parse_results(text, variant, round_number):
    rows = []
    for line in text.splitlines():
        if not line.startswith("BenchmarkAblation"):
            continue
        fields = line.split()
        benchmark = fields[0].rsplit("-", 1)[0]
        if benchmark not in BENCHMARKS or len(fields[2:]) % 2:
            raise RuntimeError(f"Unexpected benchmark output: {line}")
        metrics = {fields[i + 1]: float(fields[i]) for i in range(2, len(fields), 2)}
        rows.append({"variant": variant, "round": round_number, "benchmark": benchmark,
                     "iterations": int(fields[1]), "metrics": metrics, "raw": line})
    return rows


def summarize(rows):
    result = {}
    for variant in VARIANTS:
        result[variant] = {}
        for benchmark in sorted(BENCHMARKS):
            samples = [r["metrics"] for r in rows if r["variant"] == variant and r["benchmark"] == benchmark]
            if not samples:
                continue
            stats = {}
            for metric in samples[0]:
                values = [sample[metric] for sample in samples]
                stats[metric] = {"median": statistics.median(values), "min": min(values), "max": max(values)}
            result[variant][benchmark] = {"samples": len(samples), "metrics": stats}
    return result


def write_report(output, record):
    output.mkdir(parents=True, exist_ok=True)
    (output / "results.json").write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n")
    with (output / "samples.csv").open("w", newline="") as file:
        writer = csv.writer(file, lineterminator="\n")
        writer.writerow(["variant", "round", "benchmark", "iterations", "ns/op", "B/op", "allocs/op", "syncs/op", "verified/op", "timeouts/op"])
        for row in record["runs"]:
            writer.writerow([row["variant"], row["round"], row["benchmark"], row["iterations"], *[row["metrics"].get(key, "") for key in ("ns/op", "B/op", "allocs/op", "syncs/op", "verified/op", "timeouts/op")]])
    summaries = record["summary"]
    lines = ["# 性能消融实验", "", f"环境：{record['environment']['go']}；{platform.system()} {platform.machine()}。",
             f"每个变体独立编译，{record['rounds']} 轮随机顺序，固定种子 {record['seed']}；每轮每项 {record['iterations']} 次，GOMAXPROCS=4。下表为各轮每次操作耗时的中位数。", "",
             "实验使用同一份最终源码，只在临时副本关闭指定优化，不修改工作区或用户安装。来源适配器使用固定本地结果与 20 ms 延迟，无真实网络请求；文件系统缓存为热缓存，不代表冷启动或端到端网络耗时。", "",
             "| 变体 | 历史读取 ms | 来源失败恢复 ms | 来源成功恢复 ms | 退出进程锁恢复 ms |", "|---|---:|---:|---:|---:|"]
    order = ("History", "RecoveryFailed", "RecoveryVerified", "ExitedLock")
    for variant in VARIANTS:
        values = [summaries[variant].get("BenchmarkAblation" + suffix, {}).get("metrics", {}).get("ns/op", {}).get("median") for suffix in order]
        lines.append("| " + variant + " | " + " | ".join("—" if value is None else f"{value / 1e6:.3f}" for value in values) + " |")
    lines += ["", "消融含义：`no_buffer_reuse` 恢复逐行分配；`no_source_index` 每个 Skill 重新扫描来源；`no_dedup` 不合并预取请求；`serial` 将来源并发从 4 降为 1；`legacy_lock` 恢复 v0.0.3 的文件存在锁。另有去重与并发同时关闭，以及全部关闭，便于检查相互作用。`with_history_filter` 两项只在临时副本加入已放弃的工具名预筛，用于记录负面结果；该预筛不在最终程序中。", "",
              "历史场景约 17 MB、4096 条无关记录和 16 条安装记录；失败场景 32 个 Skill 共享 8 个来源，每次模拟 20 ms 失败；成功场景 20 个已安装 Skill 共享含 200 个 Skill 的来源。每轮均检查候选身份、验证数量和失败后不登记来源。", "",
              "锁场景使用真实退出的子进程 PID 和 50 ms 等待预算。旧锁预期超时，新锁预期成功；这项同时比较正确性，不能将超时截断值当成完成耗时。", "",
              "完整逐轮指标、内存分配、调用次数及原始 benchmark 行见 `results.json` 和 `samples.csv`。`all_off` 是受控算法基线，并非整个 v0.0.3 二进制。"]
    (output / "report.md").write_text("\n".join(lines) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=ROOT / "benchmarks/results")
    parser.add_argument("--rounds", type=int, default=7)
    parser.add_argument("--iterations", type=int, default=3)
    parser.add_argument("--seed", type=int, default=77334)
    args = parser.parse_args()
    if args.rounds < 1 or args.iterations < 1:
        parser.error("rounds and iterations must be positive")
    output = args.output.resolve()
    sources = sorted([ROOT / "go.mod", ROOT / "go.sum", ROOT / "main.go", *ROOT.glob("internal/**/*.go")])
    digest = hashlib.sha256()
    for path in sources:
        digest.update(path.relative_to(ROOT).as_posix().encode() + b"\0" + path.read_bytes())
    env = dict(os.environ, GOMAXPROCS="4")
    record = {"environment": {"go": run(["go", "version"], ROOT, env).strip(), "platform": platform.platform(), "machine": platform.machine()},
              "source_sha256": digest.hexdigest(), "rounds": args.rounds, "iterations": args.iterations,
              "seed": args.seed, "disabled": {key: sorted(value) for key, value in VARIANTS.items()},
              "experimental_additions": {key: ["history_filter"] for key in VARIANTS if key.startswith("with_history_filter")}, "runs": []}
    started = time.perf_counter()
    with tempfile.TemporaryDirectory(prefix="skillctl-ablation-") as temporary:
        temp = Path(temporary)
        # Keep build caches caller-configurable; default Go cache contents are only read.
        env.setdefault("GOCACHE", str(temp / "go-cache"))
        binaries = {}
        for variant, disabled in VARIANTS.items():
            path = temp / variant
            for source in sources:
                target = path / source.relative_to(ROOT)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(source, target)
            apply_variant(path, disabled, variant)
            binary_dir = path / "bin"
            binary_dir.mkdir()
            run(["go", "test", "-c", "-tags=ablation", "-trimpath", "-o", str(binary_dir), *["./internal/" + name for name in PACKAGES]], path, env)
            suffix = ".test.exe" if os.name == "nt" else ".test"
            binaries[variant] = [binary_dir / (name + suffix) for name in PACKAGES]
            print(f"Built {variant}", flush=True)
        randomizer = random.Random(args.seed)
        for round_number in range(1, args.rounds + 1):
            order = list(VARIANTS)
            randomizer.shuffle(order)
            for variant in order:
                rows = []
                for binary in binaries[variant]:
                    text = run([str(binary), "-test.run=^$", "-test.bench=^BenchmarkAblation", f"-test.benchtime={args.iterations}x", "-test.count=1", "-test.cpu=4", "-test.timeout=90s"], binary.parent, env)
                    rows.extend(parse_results(text, variant, round_number))
                if {row["benchmark"] for row in rows} != BENCHMARKS:
                    raise RuntimeError(f"Incomplete measurements: {variant}, round {round_number}")
                record["runs"].extend(rows)
            print(f"Completed round {round_number}/{args.rounds}", flush=True)
    record["elapsed_seconds"] = round(time.perf_counter() - started, 3)
    record["summary"] = summarize(record["runs"])
    write_report(output, record)
    print(f"Results: {output / 'report.md'}", flush=True)


if __name__ == "__main__":
    main()
