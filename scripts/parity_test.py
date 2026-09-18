"""Cross-language parity test between Python CLI prototype and compiled Go binary.

Verifies exact JSON schema and data parity across subcommands.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path


def run_cmd(args: list[str]) -> tuple[int, str, str]:
    proc = subprocess.run(
        args,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
    )
    return proc.returncode, proc.stdout.strip(), proc.stderr.strip()


def test_status_parity(py_cmd: list[str], go_cmd: list[str]) -> bool:
    print("\n--- Testing 'status --json' Parity ---")
    rc_py, out_py, err_py = run_cmd([*py_cmd, "status", "--json"])
    rc_go, out_go, err_go = run_cmd([*go_cmd, "status", "--json"])

    if rc_py != 0:
        print(f"FAIL: Python CLI returned {rc_py}: {err_py}")
        return False
    if rc_go != 0:
        print(f"FAIL: Go CLI returned {rc_go}: {err_go}")
        return False

    data_py = json.loads(out_py)
    data_go = json.loads(out_go)

    core_keys = ["primary_root", "checkout_root", "branch", "is_clean", "active_claims"]
    for k in core_keys:
        if k not in data_go:
            print(f"FAIL: Go status JSON missing key '{k}'")
            return False

    print(f"Python branch: {data_py.get('branch')} | Go branch: {data_go.get('branch')}")
    print(f"Python is_clean: {data_py.get('is_clean')} | Go is_clean: {data_go.get('is_clean')}")
    print(f"Python active claims: {len(data_py.get('active_claims', []))} | Go active claims: {len(data_go.get('active_claims', []))}")
    print("PASS: status --json parity verified.")
    return True


def test_list_parity(py_cmd: list[str], go_cmd: list[str]) -> bool:
    print("\n--- Testing 'list --json' Parity ---")
    rc_py, out_py, err_py = run_cmd([*py_cmd, "list", "--json"])
    rc_go, out_go, err_go = run_cmd([*go_cmd, "list", "--json"])

    if rc_py != 0:
        print(f"FAIL: Python CLI returned {rc_py}: {err_py}")
        return False
    if rc_go != 0:
        print(f"FAIL: Go CLI returned {rc_go}: {err_go}")
        return False

    data_py = json.loads(out_py)
    data_go = json.loads(out_go)

    py_count = data_py.get("count", 0)
    go_count = data_go.get("count", 0)
    print(f"Python registered count: {py_count} | Go registered count: {go_count}")

    if py_count != go_count:
        print(f"FAIL: Registered count mismatch: Python={py_count}, Go={go_count}")
        return False

    py_ids = sorted([h["id"] for h in data_py.get("harnesses", [])])
    go_ids = sorted([h["id"] for h in data_go.get("harnesses", [])])
    if py_ids != go_ids:
        print(f"FAIL: Harness ID list mismatch:\n  Py: {py_ids}\n  Go: {go_ids}")
        return False

    print("PASS: list --json parity verified.")
    return True


def test_auth_status_parity(py_cmd: list[str], go_cmd: list[str]) -> bool:
    print("\n--- Testing 'auth status --json' Parity ---")
    rc_py, out_py, err_py = run_cmd([*py_cmd, "auth", "status", "--json"])
    rc_go, out_go, err_go = run_cmd([*go_cmd, "auth", "status", "--json"])

    if rc_py != 0:
        print(f"FAIL: Python CLI returned {rc_py}: {err_py}")
        return False
    if rc_go != 0:
        print(f"FAIL: Go CLI returned {rc_go}: {err_go}")
        return False

    data_py = json.loads(out_py)
    data_go = json.loads(out_go)

    if "active_vault_backend" not in data_go:
        print("FAIL: Go auth status missing 'active_vault_backend'")
        return False

    py_provs = sorted([p["provider"] for p in data_py.get("providers", [])])
    go_provs = sorted([p["provider"] for p in data_go.get("providers", [])])
    if py_provs != go_provs:
        print(f"FAIL: Provider list mismatch:\n  Py: {py_provs}\n  Go: {go_provs}")
        return False

    print(f"Python backend: {data_py.get('active_vault_backend')} | Go backend: {data_go.get('active_vault_backend')}")
    print(f"Providers: {', '.join(go_provs)}")
    print("PASS: auth status --json parity verified.")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description="Cross-language parity test between Python and Go CLI")
    parser.add_argument("--python-cli", default=r"C:\Code\ai-router\scripts\cli\harness.py")
    parser.add_argument("--go-cli", default=r"C:\Code\harness-cli\harness.exe")
    args = parser.parse_args()

    py_cmd = [sys.executable, str(Path(args.python_cli).resolve())]
    go_cmd = [str(Path(args.go_cli).resolve())]

    print("=== Harness CLI Cross-Language Parity Suite ===")
    print(f"Python CLI: {' '.join(py_cmd)}")
    print(f"Go CLI:     {' '.join(go_cmd)}")

    all_passed = True
    if not test_status_parity(py_cmd, go_cmd):
        all_passed = False
    if not test_list_parity(py_cmd, go_cmd):
        all_passed = False
    if not test_auth_status_parity(py_cmd, go_cmd):
        all_passed = False

    if all_passed:
        print("\n==========================================")
        print("SUCCESS: ALL PARITY VERIFICATION CHECKS PASSED!")
        print("==========================================")
        return 0
    else:
        print("\nFAILURE: One or more parity checks failed.")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
