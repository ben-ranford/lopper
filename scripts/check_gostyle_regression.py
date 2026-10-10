#!/usr/bin/env python3
"""Verify the anonymous-context regression, then run the ordinary gostyle scan."""
import argparse
from collections import Counter
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time

MODULE = "github.com/k1LoW/gostyle"
TOOL_TIMEOUT = 300
FIXTURE_TIMEOUT = 60


class GateError(Exception):
    """A required tool or diagnostic contract failed."""


def execute(command, cwd, environment, timeout=None):
    return subprocess.run(command, cwd=cwd, env=environment, timeout=timeout,
                          capture_output=True, text=True, errors="replace", check=False)


def checked(command, cwd, environment, timeout):
    result = execute(command, cwd, environment, timeout)
    if result.returncode:
        raise GateError(f"command failed ({result.returncode}): {command}\n{result.stdout}{result.stderr}")
    return result.stdout


def resolve_compiler(launcher, toolchain, root):
    environment = dict(os.environ, GOTOOLCHAIN=toolchain)
    data = json.loads(checked([launcher, "env", "-json", "GOROOT", "GOEXE", "GOMODCACHE", "GOCACHE"],
                              root, environment, FIXTURE_TIMEOUT))
    compiler = Path(data["GOROOT"]) / "bin" / ("go" + data["GOEXE"])
    environment.update(GOTOOLCHAIN="local", GOENV="off", GOWORK="off", GOFLAGS="-buildvcs=false",
                       GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org", GOAUTH="off",
                       GOPRIVATE="", GONOPROXY="", GONOSUMDB="", GOINSECURE="",
                       GOMODCACHE=data["GOMODCACHE"], GOCACHE=data["GOCACHE"],
                       PATH=str(compiler.parent) + os.pathsep + os.environ.get("PATH", ""))
    for name in ("GOROOT", "GOCACHEPROG"):
        environment.pop(name, None)
    return str(compiler.resolve()), environment, data["GOEXE"]


def module_records(text):
    decoder = json.JSONDecoder()
    remaining = text.strip()
    while remaining:
        record, offset = decoder.raw_decode(remaining)
        yield record
        remaining = remaining[offset:].lstrip()


def verify_graph(text, version):
    selected = False
    for record in module_records(text):
        if "Replace" in record:
            raise GateError("tool receipt graph contains a replacement")
        if record.get("Path") == MODULE:
            if record.get("Version") != version:
                raise GateError("tool receipt selected a different version")
            selected = True
    if not selected:
        raise GateError("tool receipt is missing the configured official module")


def build_tool(compiler, environment, extension, version, temporary):
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise GateError("gostyle version must be an explicit release or pseudo-version")
    receipt = temporary / "receipt"
    receipt.mkdir()
    (receipt / "go.mod").write_text("module example.com/lopper-gostyle-receipt\n", encoding="utf-8")
    deadline = time.monotonic() + TOOL_TIMEOUT

    def run(*arguments):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise GateError("gostyle module/build budget expired")
        return checked([compiler, *arguments], receipt, environment, remaining)

    run("get", MODULE + "@" + version)
    run("mod", "download", "all")
    verify_graph(run("list", "-m", "-json", "all"), version)
    verification = run("mod", "verify")
    print(f"gostyle {version} selected graph: {verification.strip()}")
    environment = dict(environment, GOBIN=str(temporary / "bin"))
    run("install", MODULE + "@" + version)
    return temporary / "bin" / ("gostyle" + extension)


def verify_diagnostics(result, expected, fixture):
    if result.returncode != 3:
        raise GateError(f"fixture expected direct findings exit 3, got {result.returncode}")
    if result.stdout:
        raise GateError("fixture emitted unexpected stdout")
    wanted = Counter(f"{fixture / item['path']}:{item['line']}:{item['column']}: {item['message']}"
                     for item in expected)
    if Counter(result.stderr.splitlines()) != wanted:
        raise GateError("fixture diagnostics differ from the exact anonymous-context contract")


def fixture_scan(binary, config, root, environment, temporary):
    fixture = temporary / "fixture"
    shutil.copytree(root / "scripts/testdata/gostyle-regression", fixture)
    expected = json.loads((fixture / "expected.json").read_text(encoding="utf-8"))
    result = execute([str(binary), "run", "-c", str(config), "./..."], fixture,
                     environment, FIXTURE_TIMEOUT)
    try:
        verify_diagnostics(result, expected, fixture)
    except GateError as error:
        raise GateError(f"{error}\n{result.stdout}{result.stderr}") from error
    print("gostyle anonymous-context fixture: expected exit 3 and exact diagnostic; valid controls clean")


def scan_both(binary, config, root, environment, temporary):
    failed = False
    try:
        fixture_scan(binary, config, root, environment, temporary)
    except (GateError, OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        print(f"gostyle regression fixture failed: {error}", file=sys.stderr)
        failed = True
    try:
        result = execute([str(binary), "run", "-c", str(config), "./..."], root, environment)
        sys.stdout.write(result.stdout)
        sys.stderr.write(result.stderr)
        print(f"gostyle ordinary repository scan: exit {result.returncode}")
        failed = failed or result.returncode != 0
    except (OSError, subprocess.TimeoutExpired) as error:
        print(f"gostyle ordinary repository scan failed: {error}", file=sys.stderr)
        failed = True
    return int(failed)


def run(args):
    root = Path(args.root).resolve()
    config = Path(args.config).resolve()
    try:
        compiler, environment, extension = resolve_compiler(args.go, args.toolchain, root)
        with tempfile.TemporaryDirectory(prefix="lopper-gostyle-") as directory:
            temporary = Path(directory).resolve()
            if temporary.is_relative_to(root):
                raise GateError("gostyle temporary directory must be outside the source checkout")
            binary = build_tool(compiler, environment, extension, args.version, temporary)
            return scan_both(binary, config, root, environment, temporary)
    except (GateError, OSError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        print(f"gostyle regression setup failed: {error}", file=sys.stderr)
        return 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", required=True)
    parser.add_argument("--toolchain", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--config", required=True)
    parser.add_argument("--root", required=True)
    return run(parser.parse_args())


if __name__ == "__main__":
    sys.exit(main())
