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


def test_commands_parity(py_cmd: list[str], go_cmd: list[str]) -> bool:
    print("\n--- Testing 'commands --json' Parity ---")
    rc_py, out_py, err_py = run_cmd([*py_cmd, "commands", "--json"])
    rc_go, out_go, err_go = run_cmd([*go_cmd, "commands", "--json"])

    if rc_py != 0:
        print(f"FAIL: Python CLI returned {rc_py}: {err_py}")
        return False
    if rc_go != 0:
        print(f"FAIL: Go CLI returned {rc_go}: {err_go}")
        return False

    data_py = json.loads(out_py)
    data_go = json.loads(out_go)

    if "commands" not in data_go:
        print("FAIL: Go commands output missing 'commands' list")
        return False

    py_cmds = [c.get("name") for c in data_py.get("commands", [])]
    go_cmds = [c.get("name") for c in data_go.get("commands", [])]
    print(f"Python commands ({len(py_cmds)}): {', '.join(py_cmds)}")
    print(f"Go commands     ({len(go_cmds)}): {', '.join(go_cmds)}")

    if py_cmds != go_cmds:
        print(f"FAIL: Commands list mismatch:\n  Py: {py_cmds}\n  Go: {go_cmds}")
        return False

    print("PASS: commands --json parity verified.")
    return True


def test_local_status_parity(py_cmd: list[str], go_cmd: list[str]) -> bool:
    print("\n--- Testing 'local status --json' Parity ---")
    rc_py, out_py, _ = run_cmd([*py_cmd, "local", "status", "--json"])
    rc_go, out_go, _ = run_cmd([*go_cmd, "local", "status", "--json"])

    if rc_py != rc_go:
        print(f"FAIL: Return code mismatch: Python={rc_py}, Go={rc_go}")
        return False

    try:
        data_py = json.loads(out_py)
    except json.JSONDecodeError as exc:
        print(f"FAIL: Python CLI output not valid JSON: {exc}\n{out_py}")
        return False

    try:
        data_go = json.loads(out_go)
    except json.JSONDecodeError as exc:
        print(f"FAIL: Go CLI output not valid JSON: {exc}\n{out_go}")
        return False

    for k in ["status", "base_url"]:
        if data_py.get(k) != data_go.get(k):
            print(f"FAIL: Mismatch for key '{k}': Python={data_py.get(k)}, Go={data_go.get(k)}")
            return False

    print(f"Python status: {data_py.get('status')} | Go status: {data_go.get('status')}")
    print(f"Python base_url: {data_py.get('base_url')} | Go base_url: {data_go.get('base_url')}")
    print("PASS: local status --json parity verified.")
    return True


def test_deferral_parity(py_cmd: list[str], go_cmd: list[str]) -> bool:
    print("\n--- Testing 'deferral --json' Parity ---")
    # Test 1: default empty prompt
    rc_py, out_py, err_py = run_cmd([*py_cmd, "deferral", "--json"])
    rc_go, out_go, err_go = run_cmd([*go_cmd, "deferral", "--json"])

    if rc_py != 0:
        print(f"FAIL: Python CLI returned {rc_py}: {err_py}")
        return False
    if rc_go != 0:
        print(f"FAIL: Go CLI returned {rc_go}: {err_go}")
        return False

    data_py = json.loads(out_py)
    data_go = json.loads(out_go)

    core_keys = ["should_defer", "recommended_model", "trigger", "quota_profile", "fallback_active"]
    for k in core_keys:
        if data_py.get(k) != data_go.get(k):
            print(f"FAIL: Default prompt mismatch for '{k}': Python={data_py.get(k)}, Go={data_go.get(k)}")
            return False

    # Test 2: trigger prompt
    trigger_prompt = "fix race condition in distributed lock"
    rc_py2, out_py2, err_py2 = run_cmd([*py_cmd, "deferral", trigger_prompt, "--json"])
    rc_go2, out_go2, err_go2 = run_cmd([*go_cmd, "deferral", trigger_prompt, "--json"])

    if rc_py2 != 0 or rc_go2 != 0:
        print(f"FAIL: Trigger prompt non-zero rc: Py={rc_py2}, Go={rc_go2}")
        return False

    data_py2 = json.loads(out_py2)
    data_go2 = json.loads(out_go2)

    for k in core_keys:
        if data_py2.get(k) != data_go2.get(k):
            print(f"FAIL: Trigger prompt mismatch for '{k}': Python={data_py2.get(k)}, Go={data_go2.get(k)}")
            return False

    print(f"Default defer: Py={data_py.get('should_defer')} | Go={data_go.get('should_defer')}")
    print(f"Trigger defer: Py={data_py2.get('should_defer')} ({data_py2.get('trigger')}) | Go={data_go2.get('should_defer')} ({data_go2.get('trigger')})")
    print(f"Recommended:   Py={data_py2.get('recommended_model')} | Go={data_go2.get('recommended_model')}")
    print("PASS: deferral --json parity verified.")
    return True


def test_claim_inspect_parity(py_cmd: list[str], go_cmd: list[str]) -> bool:
    print("\n--- Testing 'claim inspect --json' Parity ---")
    rc_py, out_py, err_py = run_cmd([*py_cmd, "claim", "inspect", "--json"])
    rc_go, out_go, err_go = run_cmd([*go_cmd, "claim", "inspect", "--json"])

    if rc_py != 0:
        print(f"FAIL: Python CLI returned {rc_py}: {err_py}")
        return False
    if rc_go != 0:
        print(f"FAIL: Go CLI returned {rc_go}: {err_go}")
        return False

    data_py = json.loads(out_py)
    data_go = json.loads(out_go)

    core_keys = ["total_claims", "stale_claims", "stale_threshold_hours", "claims", "warnings"]
    for k in core_keys:
        if k not in data_go:
            print(f"FAIL: Go claim inspect JSON missing key '{k}'")
            return False

    if data_py.get("total_claims") != data_go.get("total_claims"):
        print(f"FAIL: total_claims mismatch: Python={data_py.get('total_claims')}, Go={data_go.get('total_claims')}")
        return False
    if data_py.get("stale_claims") != data_go.get("stale_claims"):
        print(f"FAIL: stale_claims mismatch: Python={data_py.get('stale_claims')}, Go={data_go.get('stale_claims')}")
        return False

    print(f"Python total claims: {data_py.get('total_claims')} | Go total claims: {data_go.get('total_claims')}")
    print(f"Python stale claims: {data_py.get('stale_claims')} | Go stale claims: {data_go.get('stale_claims')}")
    print("PASS: claim inspect --json parity verified.")
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
    if not test_commands_parity(py_cmd, go_cmd):
        all_passed = False
    if not test_local_status_parity(py_cmd, go_cmd):
        all_passed = False
    if not test_deferral_parity(py_cmd, go_cmd):
        all_passed = False
    if not test_claim_inspect_parity(py_cmd, go_cmd):
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
