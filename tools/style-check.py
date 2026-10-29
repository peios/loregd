#!/usr/bin/env python3
"""Check maintained sources against the pinned PSC language profiles."""

import os
import shutil
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def tool(name: str, expected: str) -> str:
    directory = os.environ.get("PEIOS_STYLE_BIN")
    path = str(Path(directory) / name) if directory else shutil.which(name)
    if path is None:
        raise SystemExit(f"missing {name}; run tools/prepare-style-tools.py OUTSIDE_REPO")
    version = subprocess.check_output([path, "--version"], text=True)
    if expected not in version.splitlines():
        raise SystemExit(f"unexpected {name} version: {version.strip()}")
    return path


def run(args: list[str]) -> None:
    subprocess.run(args, cwd=ROOT, check=True)


def main() -> None:
    go = sorted(str(p.relative_to(ROOT)) for p in ROOT.glob("*.go"))
    go += sorted(str(p.relative_to(ROOT)) for p in (ROOT / "internal").rglob("*.go"))
    changed = subprocess.check_output(
        [os.environ.get("GOFMT", "gofmt"), "-l", *go], cwd=ROOT, text=True
    )
    if changed:
        raise SystemExit("gofmt changes required:\n" + changed)
    python = sorted(str(p.relative_to(ROOT)) for p in (ROOT / "tools").glob("*.py"))
    if (ROOT / "tools/check-docs").exists():
        python.append("tools/check-docs")
    ruff = tool("ruff", "ruff 0.17.0")
    run([ruff, "check", "--no-cache", "--config", "tools/ruff.toml", *python])
    run([ruff, "format", "--check", "--no-cache", "--config", "tools/ruff.toml", *python])
    shell = ["tools/check", "tests/integration/check-integration"]
    run([tool("shellcheck", "version: 0.11.0"), *shell])
    run([tool("shfmt", "v3.14.1"), "-d", "-i", "4", *shell])
    data = sorted(
        str(p.relative_to(ROOT))
        for root in (".github",)
        for p in (ROOT / root).rglob("*")
        if p.suffix in {".json", ".yml"}
    )
    if data:
        run([tool("prettier", "3.9.10"), "--config", "tools/prettier.json", "--check", *data])


if __name__ == "__main__":
    main()
