"""Measure fresh-process startup with the Python standard library.

tags: [benchmarks, cli, startup, methodology]
routing_hints: [harness-cli, startup, benchmark]
"""

from __future__ import annotations

import argparse
import json
import os
import statistics
import subprocess
import tempfile
import time
from pathlib import Path


def percentile_linear(samples: list[float], percentile: float) -> float:
    ordered = sorted(samples)
    position = (len(ordered) - 1) * percentile
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    weight = position - lower
    return ordered[lower] * (1.0 - weight) + ordered[upper] * weight


def build_minimal_go_control(output_path: Path) -> None:
    output_path = output_path.resolve()
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="harness-startup-control-") as temp_dir:
        source_path = Path(temp_dir) / "main.go"
        source_path.write_text("package main\n\nfunc main() {}\n", encoding="utf-8")
        env = dict(os.environ)
        env["CGO_ENABLED"] = "0"
        subprocess.run(
            ["go", "build", "-trimpath", "-o", str(output_path), str(source_path)],
            check=True,
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
        )
    print(json.dumps({"minimal_go_control_built": True, "output_name": output_path.name}, separators=(",", ":")))


def launch_ms(command: list[str], timeout: float) -> float:
    start = time.perf_counter_ns()
    result = subprocess.run(
        command,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
        timeout=timeout,
    )
    elapsed_ms = (time.perf_counter_ns() - start) / 1_000_000
    if result.returncode != 0:
        raise RuntimeError(f"command exited with status {result.returncode}")
    return elapsed_ms


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--label", help="Safe, path-free target label for output")
    parser.add_argument("--warm-runs", type=int, default=20)
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--build-minimal-go-control", metavar="OUTPUT_PATH")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()

    if args.build_minimal_go_control:
        if args.command:
            parser.error("do not provide a command when building the minimal Go control")
        build_minimal_go_control(Path(args.build_minimal_go_control))
        return 0

    command = args.command[1:] if args.command and args.command[0] == "--" else args.command
    if not args.label:
        parser.error("--label is required when measuring a command")
    if not command:
        parser.error("provide a command after --")
    if args.warm_runs < 1:
        parser.error("--warm-runs must be positive")

    first_ms = launch_ms(command, args.timeout)
    warm_ms = [launch_ms(command, args.timeout) for _ in range(args.warm_runs)]
    result = {
        "label": args.label,
        "first_ms": round(first_ms, 3),
        "warm_ms": [round(value, 3) for value in warm_ms],
        "warm_summary_ms": {
            "count": len(warm_ms),
            "median": round(statistics.median(warm_ms), 3),
            "mean": round(statistics.fmean(warm_ms), 3),
            "p95_linear": round(percentile_linear(warm_ms, 0.95), 3),
        },
    }
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
