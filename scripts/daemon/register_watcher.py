"""Configure and manage Harness Claim Watcher daemon across Windows, macOS, and Linux.

tags: [daemon, watcher, claims, worktree, cross-platform]
routing_hints: [schtasks, launchd, systemd, watcher, daemon, background]
"""

from __future__ import annotations

import argparse
import os
import plistlib
import shutil
import subprocess
import sys
from pathlib import Path


def find_harness_binary() -> str | None:
    """Locate the compiled harness CLI binary across Windows and POSIX platforms."""
    bin_name = "harness.exe" if os.name == "nt" else "harness"
    cmd = shutil.which("harness") or shutil.which(bin_name)
    if cmd:
        return cmd
    candidates: list[Path] = [
        # POSIX standard paths
        Path.home() / ".local" / "bin" / bin_name,
        Path("/opt/homebrew/bin") / bin_name,
        Path("/usr/local/bin") / bin_name,
    ]
    if os.name == "nt":
        candidates.extend([
            Path.home() / "scoop" / "shims" / bin_name,
            Path(os.environ.get("LOCALAPPDATA", "")) / "Programs" / "harness" / bin_name,
            Path("C:/Code/harness-cli") / bin_name,
        ])
    # Relative to this script in harness-cli repo
    repo_root = Path(__file__).resolve().parents[2]
    candidates.append(repo_root / bin_name)

    for cand in candidates:
        if cand.is_file():
            return str(cand)
    return None


def register_windows(
    task_name: str,
    harness_path: str,
    interval_minutes: int,
    stale_hours: float,
    auto_clean: bool,
) -> int:
    """Register or replace Windows Scheduled Task using schtasks.exe."""
    arg_parts = ["claim", "watch", "--interval", f"{interval_minutes}m", "--stale-hours", str(stale_hours)]
    if auto_clean:
        arg_parts.append("--auto-clean")

    full_cmd = f'"{harness_path}" {" ".join(arg_parts)}'

    subprocess.run(
        ["schtasks", "/Delete", "/TN", task_name, "/F"],
        capture_output=True,
        check=False,
    )

    cmd = [
        "schtasks",
        "/Create",
        "/TN",
        task_name,
        "/TR",
        full_cmd,
        "/SC",
        "MINUTE",
        "/MO",
        str(interval_minutes),
        "/F",
    ]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        print(f"error: failed to register scheduled task: {proc.stderr or proc.stdout}", file=sys.stderr)
        return proc.returncode

    print(f"Successfully registered Windows Scheduled Task '{task_name}'.")
    print(f"Command: {full_cmd}")
    print(f"Interval: {interval_minutes}m | Stale threshold: {stale_hours}h | Auto-clean: {auto_clean}")
    return 0


def status_windows(task_name: str) -> int:
    """Query Windows Scheduled Task status."""
    proc = subprocess.run(
        ["schtasks", "/Query", "/TN", task_name, "/FO", "LIST", "/V"],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        print(f"Scheduled task '{task_name}' is NOT registered.", file=sys.stderr)
        return 1

    print(f"=== Windows Scheduled Task Status: {task_name} ===")
    print(proc.stdout.strip())
    return 0


def unregister_windows(task_name: str) -> int:
    """Remove Windows Scheduled Task."""
    proc = subprocess.run(
        ["schtasks", "/Delete", "/TN", task_name, "/F"],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        print(f"Scheduled task '{task_name}' is not registered; nothing to remove.", file=sys.stderr)
        return 0

    print(f"Successfully unregistered scheduled task '{task_name}'.")
    return 0


def get_macos_plist_path(task_name: str) -> Path:
    label = f"com.harness.{task_name.lower()}"
    return Path.home() / "Library" / "LaunchAgents" / f"{label}.plist"


def register_macos(
    task_name: str,
    harness_path: str,
    interval_minutes: int,
    stale_hours: float,
    auto_clean: bool,
) -> int:
    """Register macOS launchd LaunchAgent user daemon."""
    plist_path = get_macos_plist_path(task_name)
    plist_path.parent.mkdir(parents=True, exist_ok=True)
    label = f"com.harness.{task_name.lower()}"

    args = [harness_path, "claim", "watch", "--interval", f"{interval_minutes}m", "--stale-hours", str(stale_hours)]
    if auto_clean:
        args.append("--auto-clean")

    if plist_path.is_file():
        subprocess.run(["launchctl", "unload", "-w", str(plist_path)], capture_output=True, check=False)

    log_dir = Path.home() / "Library" / "Logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    out_log = str(log_dir / f"{label}.out.log")
    err_log = str(log_dir / f"{label}.err.log")

    plist_data = {
        "Label": label,
        "ProgramArguments": args,
        "RunAtLoad": True,
        "StartInterval": interval_minutes * 60,
        "StandardOutPath": out_log,
        "StandardErrorPath": err_log,
    }

    with open(plist_path, "wb") as f:
        plistlib.dump(plist_data, f)

    proc = subprocess.run(["launchctl", "load", "-w", str(plist_path)], capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        print(f"error: launchctl load failed: {proc.stderr or proc.stdout}", file=sys.stderr)
        return proc.returncode

    print(f"Successfully registered macOS LaunchAgent '{label}'.")
    print(f"Plist path: {plist_path}")
    print(f"Interval: {interval_minutes}m | Stale threshold: {stale_hours}h | Auto-clean: {auto_clean}")
    return 0


def status_macos(task_name: str) -> int:
    """Query macOS LaunchAgent status."""
    label = f"com.harness.{task_name.lower()}"
    plist_path = get_macos_plist_path(task_name)
    if not plist_path.is_file():
        print(f"macOS LaunchAgent '{label}' is NOT registered.", file=sys.stderr)
        return 1

    proc = subprocess.run(["launchctl", "list", label], capture_output=True, text=True, check=False)
    print(f"=== macOS LaunchAgent Status: {label} ===")
    print(f"Plist: {plist_path}")
    if proc.returncode == 0:
        print(proc.stdout.strip())
    else:
        print("Agent registered but not active / loaded.")
    return 0


def unregister_macos(task_name: str) -> int:
    """Unload and remove macOS LaunchAgent."""
    plist_path = get_macos_plist_path(task_name)
    label = f"com.harness.{task_name.lower()}"
    if not plist_path.is_file():
        print(f"LaunchAgent '{label}' is not registered; nothing to remove.")
        return 0

    subprocess.run(["launchctl", "unload", "-w", str(plist_path)], capture_output=True, check=False)
    plist_path.unlink(missing_ok=True)
    print(f"Successfully unregistered macOS LaunchAgent '{label}'.")
    return 0


def get_linux_unit_paths(task_name: str) -> tuple[Path, Path]:
    base_dir = Path.home() / ".config" / "systemd" / "user"
    base_name = f"harness-{task_name.lower()}"
    return base_dir / f"{base_name}.service", base_dir / f"{base_name}.timer"


def register_linux(
    task_name: str,
    harness_path: str,
    interval_minutes: int,
    stale_hours: float,
    auto_clean: bool,
) -> int:
    """Register Linux systemd user service and timer."""
    service_path, timer_path = get_linux_unit_paths(task_name)
    service_path.parent.mkdir(parents=True, exist_ok=True)
    timer_name = timer_path.name

    args = [harness_path, "claim", "watch", "--interval", f"{interval_minutes}m", "--stale-hours", str(stale_hours)]
    if auto_clean:
        args.append("--auto-clean")

    service_content = f"""[Unit]
Description=Harness Claim Watcher ({task_name})
After=network.target

[Service]
Type=oneshot
ExecStart={" ".join(args)}
"""

    timer_content = f"""[Unit]
Description=Harness Claim Watcher Timer ({task_name})

[Timer]
OnBootSec=1m
OnUnitActiveSec={interval_minutes}m
Persistent=true

[Install]
WantedBy=timers.target
"""

    service_path.write_text(service_content, encoding="utf-8")
    timer_path.write_text(timer_content, encoding="utf-8")

    subprocess.run(["systemctl", "--user", "daemon-reload"], capture_output=True, check=False)
    proc = subprocess.run(
        ["systemctl", "--user", "enable", "--now", timer_name],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        print(f"error: failed to enable systemd timer: {proc.stderr or proc.stdout}", file=sys.stderr)
        return proc.returncode

    print(f"Successfully registered Linux systemd user timer '{timer_name}'.")
    print(f"Service: {service_path}")
    print(f"Timer:   {timer_path}")
    print(f"Interval: {interval_minutes}m | Stale threshold: {stale_hours}h | Auto-clean: {auto_clean}")
    return 0


def status_linux(task_name: str) -> int:
    """Query Linux systemd user timer status."""
    _, timer_path = get_linux_unit_paths(task_name)
    timer_name = timer_path.name
    if not timer_path.is_file():
        print(f"systemd timer '{timer_name}' is NOT registered.", file=sys.stderr)
        return 1

    proc = subprocess.run(
        ["systemctl", "--user", "status", timer_name],
        capture_output=True,
        text=True,
        check=False,
    )
    print(f"=== Linux systemd User Timer Status: {timer_name} ===")
    print(proc.stdout.strip() or proc.stderr.strip())
    return 0


def unregister_linux(task_name: str) -> int:
    """Disable and remove Linux systemd user units."""
    service_path, timer_path = get_linux_unit_paths(task_name)
    timer_name = timer_path.name
    if not timer_path.is_file() and not service_path.is_file():
        print(f"systemd units for '{task_name}' are not registered; nothing to remove.")
        return 0

    subprocess.run(["systemctl", "--user", "disable", "--now", timer_name], capture_output=True, check=False)
    service_path.unlink(missing_ok=True)
    timer_path.unlink(missing_ok=True)
    subprocess.run(["systemctl", "--user", "daemon-reload"], capture_output=True, check=False)
    print(f"Successfully unregistered systemd units for '{task_name}'.")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Configure and manage the Harness Claim Watcher daemon across platforms."
    )
    parser.add_argument("--task-name", "-n", default="HarnessClaimWatcher", help="Name or label for the watcher task")
    parser.add_argument("--interval-minutes", "-i", type=int, default=15, help="Polling interval in minutes (default: 15)")
    parser.add_argument("--stale-hours", "-s", type=float, default=24.0, help="Stale claim threshold in hours (default: 24)")
    parser.add_argument("--auto-clean", action="store_true", help="Automatically prune expired or merged worktrees")
    parser.add_argument("--harness-path", default="", help="Path to harness executable (auto-detected if omitted)")
    parser.add_argument("--status", action="store_true", help="Query current daemon task status")
    parser.add_argument("--unregister", action="store_true", help="Unregister and remove daemon task")

    args = parser.parse_args(argv)

    is_windows = sys.platform == "win32"
    is_macos = sys.platform == "darwin"
    is_linux = sys.platform.startswith("linux")

    if args.status:
        if is_windows:
            return status_windows(args.task_name)
        elif is_macos:
            return status_macos(args.task_name)
        elif is_linux:
            return status_linux(args.task_name)
        else:
            print(f"error: unsupported platform '{sys.platform}'", file=sys.stderr)
            return 1

    if args.unregister:
        if is_windows:
            return unregister_windows(args.task_name)
        elif is_macos:
            return unregister_macos(args.task_name)
        elif is_linux:
            return unregister_linux(args.task_name)
        else:
            print(f"error: unsupported platform '{sys.platform}'", file=sys.stderr)
            return 1

    # Registration path
    harness_path = args.harness_path
    if not harness_path:
        detected = find_harness_binary()
        if detected:
            harness_path = detected
        else:
            print("error: unable to locate harness binary. Pass --harness-path explicitly.", file=sys.stderr)
            return 1

    if is_windows:
        return register_windows(
            args.task_name,
            harness_path,
            args.interval_minutes,
            args.stale_hours,
            args.auto_clean,
        )
    elif is_macos:
        return register_macos(
            args.task_name,
            harness_path,
            args.interval_minutes,
            args.stale_hours,
            args.auto_clean,
        )
    elif is_linux:
        return register_linux(
            args.task_name,
            harness_path,
            args.interval_minutes,
            args.stale_hours,
            args.auto_clean,
        )
    else:
        print(f"error: unsupported platform '{sys.platform}'", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
