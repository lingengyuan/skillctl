#!/usr/bin/env python3
"""Read-only history-corpus ablation; save only counts, hashes and measurements."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import statistics
import tempfile
import time

import ablation


PROBE = r'''package main
import (
    "crypto/sha256"
    "fmt"
    "os"
    "runtime"
    "slices"
    "strings"
    "time"
    "github.com/lingengyuan/skillctl/internal/installhistory"
)
func main() {
    runtime.GC()
    var before, after runtime.MemStats
    runtime.ReadMemStats(&before)
    started := time.Now()
    result, err := installhistory.ReadRoots([]string{os.Args[1]})
    elapsed := time.Since(started)
    runtime.ReadMemStats(&after)
    if err != nil { panic(err) }
    var records []string
    for _, candidates := range result {
        for _, c := range candidates { records = append(records, c.Name+"\x00"+c.Source+"\x00"+c.Ref+"\x00"+c.SkillPath) }
    }
    slices.Sort(records)
    hash := sha256.Sum256([]byte(strings.Join(records,"\n")))
    fmt.Printf("{\"seconds\":%.9f,\"alloc_bytes\":%d,\"candidates\":%d,\"digest\":\"%x\"}\n",elapsed.Seconds(),after.TotalAlloc-before.TotalAlloc,len(records),hash)
}
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, action="append", required=True)
    parser.add_argument("--output", type=Path, default=ablation.ROOT / "benchmarks/results/local-history.json")
    parser.add_argument("--rounds", type=int, default=7)
    parser.add_argument("--exclude-recent-seconds", type=int, default=300)
    args = parser.parse_args()
    if args.rounds < 1 or args.exclude_recent_seconds < 0:
        parser.error("rounds must be positive and exclude-recent-seconds nonnegative")
    manifest = []
    recent = 0
    seen = set()
    cutoff = time.time() - args.exclude_recent_seconds
    for origin in args.root:
        for path in sorted(origin.expanduser().resolve().rglob("*.jsonl")):
            path = path.resolve()
            if path in seen:
                continue
            seen.add(path)
            info = path.stat()
            if info.st_mtime >= cutoff:
                recent += 1
                continue
            manifest.append((path, info.st_size, info.st_mtime_ns, info.st_ino))
    if not manifest:
        parser.error("no stable JSONL files were found")
    env = dict(os.environ, GOMAXPROCS="4")
    variants = {"before": {"buffer_reuse"}, "buffer_reuse": set(), "candidate_filter": set()}
    rows = []
    with tempfile.TemporaryDirectory(prefix="skillctl-history-ablation-") as temporary:
        temp = Path(temporary)
        env.setdefault("GOCACHE", str(temp / "go-cache"))
        corpus = temp / "corpus"
        corpus.mkdir()
        for i, (path, _, _, _) in enumerate(manifest):
            destination = corpus / f"{i:06d}.jsonl"
            if os.name == "nt":
                shutil.copy2(path, destination)
            else:
                destination.symlink_to(path)
        print(f"Corpus: {len(manifest)} files, {sum(x[1] for x in manifest)} bytes; excluded {recent} recent files", flush=True)
        binaries = {}
        for variant, disabled in variants.items():
            root = temp / variant
            shutil.copytree(ablation.ROOT / "internal/installhistory", root / "internal/installhistory")
            for name in ("go.mod", "go.sum"):
                shutil.copy2(ablation.ROOT / name, root / name)
            ablation.apply_variant(root, disabled, "with_history_filter" if variant == "candidate_filter" else variant)
            target = root / "cmd/historyprobe"
            target.mkdir(parents=True)
            (target / "main.go").write_text(PROBE)
            binary = root / ("historyprobe.exe" if os.name == "nt" else "historyprobe")
            ablation.run(["go", "build", "-trimpath", "-o", str(binary), "./cmd/historyprobe"], root, env)
            binaries[variant] = binary
        randomizer = random.Random(77334)
        for round_number in range(1, args.rounds + 1):
            order = list(variants)
            randomizer.shuffle(order)
            for variant in order:
                value = json.loads(ablation.run([str(binaries[variant]), str(corpus)], temp, env))
                value.update(variant=variant, round=round_number)
                rows.append(value)
            print(f"History round {round_number}/{args.rounds}", flush=True)
        for path, size, mtime, inode in manifest:
            info = path.stat()
            if (info.st_size, info.st_mtime_ns, info.st_ino) != (size, mtime, inode):
                raise RuntimeError("A corpus file changed during measurement; results are invalid. Retry with a longer exclusion period.")
        if len({row["digest"] for row in rows}) != 1:
            raise RuntimeError("Candidate identities differ across variants")
    source_digest = hashlib.sha256()
    for source in sorted((ablation.ROOT / "internal/installhistory").glob("*.go")):
        source_digest.update(source.name.encode() + b"\0" + source.read_bytes())
    report = {"environment": {"go": ablation.run(["go", "version"], ablation.ROOT, env).strip(), "gomaxprocs": 4},
              "source_sha256": source_digest.hexdigest(), "files": len(manifest), "bytes": sum(x[1] for x in manifest),
              "excluded_recent": recent, "exclude_recent_seconds": args.exclude_recent_seconds, "rounds": args.rounds,
              "corpus_unchanged": True, "same_candidates": True, "samples": rows,
              "summary": {variant: {key: statistics.median(row[key] for row in rows if row["variant"] == variant)
                                     for key in ("seconds", "alloc_bytes", "candidates")} for variant in variants}}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report["summary"], indent=2), flush=True)


if __name__ == "__main__":
    main()
