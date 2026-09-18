"""Startup latency benchmark: Go binary vs Python prototype.

Measures cold startup overhead across multiple iterations and asserts
that Go startup overhead is strictly sub-50ms.
"""

from __future__ import annotations

import argparse
import statistics
import subprocess
import sys
import time
from pathlib import Path


def measure_runs(cmd: list[str], runs: int = 20) -> list[float]:
    latencies = []
    for _ in range(runs):
        start = time.perf_counter()
        res = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            check=False,
        )
        elapsed_ms = (time.perf_counter() - start) * 1000.0
        if res.returncode != 0:
            raise RuntimeError(f"Command failed with code {res.returncode}: {res.stderr}")
        latencies.append(elapsed_ms)
    return latencies


def summarize(latencies: list[float]) -> dict[str, float]:
    sorted_lats = sorted(latencies)
    p95_idx = int(len(sorted_lats) * 0.95)
    return {
        "min": min(latencies),
        "max": max(latencies),
        "mean": statistics.mean(latencies),
        "median": statistics.median(latencies),
        "p95": sorted_lats[min(p95_idx, len(sorted_lats) - 1)],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Benchmark cold startup latency")
    parser.add_argument("--go-cli", default=r"C:\Code\harness-cli\harness.exe")
    parser.add_argument("--python-cli", default=r"C:\Code\ai-router\scripts\cli\harness.py")
    parser.add_argument("--runs", type=int, default=20)
    args = parser.parse_args()

    go_path = Path(args.go_cli).resolve()
    py_path = Path(args.python_cli).resolve()

    print(f"=== Cold Startup Latency Benchmark ({args.runs} runs) ===")
    print(f"Go binary:     {go_path}")
    print(f"Python script: {py_path}\n")

    subcommands = [
        ["--version"],
        ["auth", "status", "--json"],
        ["status", "--json"],
        ["list", "--json"],
    ]

    cold_startup_passed = True

    for sub in subcommands:
        sub_name = " ".join(sub)
        print(f"--- Benchmarking '{sub_name}' ---")

        go_cmd = [str(go_path), *sub]
        py_cmd = [sys.executable, str(py_path), *sub] if sub != ["--version"] else [sys.executable, str(py_path), "-h"]

        print("Measuring Go binary...")
        go_lats = measure_runs(go_cmd, args.runs)
        go_stats = summarize(go_lats)

        print("Measuring Python prototype...")
        py_lats = measure_runs(py_cmd, args.runs)
        py_stats = summarize(py_lats)

        print(f"\n[Go Native Binary]")
        print(f"  Min:    {go_stats['min']:.2f} ms")
        print(f"  Max:    {go_stats['max']:.2f} ms")
        print(f"  Mean:   {go_stats['mean']:.2f} ms")
        print(f"  Median: {go_stats['median']:.2f} ms")
        print(f"  p95:    {go_stats['p95']:.2f} ms")

        print(f"\n[Python Prototype]")
        print(f"  Min:    {py_stats['min']:.2f} ms")
        print(f"  Max:    {py_stats['max']:.2f} ms")
        print(f"  Mean:   {py_stats['mean']:.2f} ms")
        print(f"  Median: {py_stats['median']:.2f} ms")
        print(f"  p95:    {py_stats['p95']:.2f} ms")

        speedup = py_stats["mean"] / max(0.01, go_stats["mean"])
        print(f"\n=> Go Speedup: {speedup:.1f}x faster ({py_stats['mean'] - go_stats['mean']:.1f} ms latency reduction)")

        if sub == ["--version"]:
            if go_stats["mean"] <= 50.0:
                print(f"TARGET MET: Go cold binary startup latency ({go_stats['mean']:.2f} ms) is strictly sub-50ms.")
            else:
                print(f"WARNING: Go cold binary startup latency ({go_stats['mean']:.2f} ms) exceeded 50ms target.")
                cold_startup_passed = False

        print()

    if cold_startup_passed:
        print("==========================================")
        print("SUCCESS: COLD STARTUP LATENCY SUB-50MS TARGET MET!")
        print("==========================================")
        return 0
    else:
        print("FAILURE: Cold startup latency exceeded 50ms threshold.")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
