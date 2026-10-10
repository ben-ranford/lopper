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


def admitted_launcher(configured):
    launcher = None
    executable = "go.exe" if os.name == "nt" else "go"
    for directory in os.get_exec_path():
        candidate = Path(directory) / executable
        if Path(directory).is_absolute() and candidate.is_file() and os.access(candidate, os.X_OK):
            launcher = candidate.resolve(strict=True)
            break
    if launcher is None:
        raise GateError("configured Go launcher is unavailable on the trusted PATH")
    if configured != "go":
        supplied = Path(configured)
        if not supplied.is_absolute() or supplied.resolve() != launcher:
            raise GateError("configured Go launcher must match the trusted PATH compiler")
    return str(launcher)


def resolve_compiler(launcher, toolchain, root):
    launcher = admitted_launcher(launcher)
    environment = dict(os.environ, GOTOOLCHAIN=toolchain)
    environment.pop("GOROOT", None)
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


def load_receipt(fixtures, version):
    reference = json.loads((fixtures / "upstream-receipt.json").read_text(encoding="utf-8"))
    if reference["Path"] != MODULE or reference["Version"] != version:
        raise GateError("configured gostyle differs from the reviewed upstream receipt")
    return reference


def verify_download(actual, reference, origin=False):
    keys = ("Path", "Version", "Sum", "GoModSum", "Origin") if origin else ("Path", "Version", "Sum", "GoModSum")
    for key in keys:
        if actual.get(key) != reference[key]:
            raise GateError(f"official module receipt differs at {key}")


def checksum_records(text):
    records = {}
    for line in text.splitlines():
        fields = line.split()
        if len(fields) != 3 or not fields[2].startswith("h1:"):
            raise GateError("invalid tool checksum receipt record")
        key = tuple(fields[:2])
        if key in records:
            raise GateError("duplicate tool checksum receipt record")
        records[key] = fields[2]
    return records


def verify_graph_versions(text, expected):
    actual = {}
    for record in module_records(text):
        if record.get("Main"):
            continue
        path = record["Path"]
        if path in actual:
            raise GateError("duplicate selected graph module")
        actual[path] = record["Version"]
    if actual != expected:
        raise GateError("selected graph module versions differ from reviewed receipt")


def verify_closure(text, checksums, reference):
    modules = {}
    for package in module_records(text):
        record = package.get("Module")
        if record is not None:
            verify_build_module(record, checksums)
            modules[record["Path"]] = record["Version"]
    for required in (reference, reference["tools"]):
        if modules.get(required["Path"]) != required["Version"]:
            raise GateError("actual build closure is missing reviewed main/importer")


def verify_build_module(record, checksums):
    if "Replace" in record:
        raise GateError("actual build closure contains a replacement")
    for suffix, field in (("", "Sum"), ("/go.mod", "GoModSum")):
        key = (record["Path"], record["Version"] + suffix)
        if key not in checksums or record.get(field) != checksums[key]:
            raise GateError(f"actual build closure differs from checksum receipt: {key}")


def binary_module(line, checksums):
    fields = line.split()
    if not fields:
        return None
    if fields[0] == "=>" or "(devel)" in fields:
        raise GateError("replacement/development tool binary rejected")
    if fields[0] not in ("mod", "dep"):
        return None
    if len(fields) != 4:
        raise GateError("invalid binary module record")
    key = tuple(fields[1:3])
    if checksums.get(key) != fields[3]:
        raise GateError("binary dependency differs from tool checksum receipt")
    return key, (fields[0], fields[3])


def verify_binary(metadata, reference, checksums):
    modules = {}
    for line in metadata.splitlines():
        record = binary_module(line, checksums)
        if record is not None:
            key, value = record
            if key in modules:
                raise GateError("duplicate binary module record")
            modules[key] = value
    for item, kind in ((reference, "mod"), (reference["tools"], "dep")):
        if modules.get((item["Path"], item["Version"])) != (kind, item["Sum"]):
            raise GateError("binary main/importer differs from reviewed receipt")


def build_tool(compiler, environment, extension, version, temporary, fixtures):
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", version, re.ASCII):
        raise GateError("gostyle version must be an explicit release or pseudo-version")
    reference = load_receipt(fixtures, version)
    checksum_text = (fixtures / "upstream-go.sum").read_text(encoding="utf-8")
    checksums = checksum_records(checksum_text)
    receipt = temporary / "receipt"
    receipt.mkdir()
    (receipt / "go.mod").write_text("module example.com/lopper-gostyle-receipt\n", encoding="utf-8")
    (receipt / "go.sum").write_text(checksum_text, encoding="utf-8")
    deadline = time.monotonic() + TOOL_TIMEOUT

    def run(*arguments):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise GateError("gostyle module/build budget expired")
        return checked([compiler, *arguments], receipt, environment, remaining)

    actual = json.loads(run("mod", "download", "-json", MODULE + "@" + version))
    verify_download(actual, reference, origin=True)
    run("get", MODULE + "@" + version)
    run("mod", "download", "all")
    graph = run("list", "-m", "-json", "all")
    verify_graph(graph, version)
    verify_graph_versions(graph, reference["graph_versions"])
    verify_closure(run("list", "-deps", "-json", MODULE), checksums, reference)
    importer = reference["tools"]
    verify_download(json.loads(run("mod", "download", "-json", importer["Path"] + "@" + importer["Version"])), importer)
    verification = run("mod", "verify")
    print(f"gostyle {version} selected graph: {verification.strip()}")
    environment = dict(environment, GOBIN=str(temporary / "bin"))
    run("install", MODULE + "@" + version)
    binary = temporary / "bin" / ("gostyle" + extension)
    verify_binary(run("version", "-m", str(binary)), reference, checksums)
    return binary


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
    (fixture / "anonymous/fixture_test.go.txt").rename(fixture / "anonymous/fixture_test.go")
    (fixture / "multiple/fixture.go.txt").rename(fixture / "multiple/fixture.go")
    expected = json.loads((fixture / "expected.json").read_text(encoding="utf-8"))
    result = execute([str(binary), "run", "-c", str(config), "./..."], fixture,
                     environment, FIXTURE_TIMEOUT)
    try:
        verify_diagnostics(result, expected, fixture)
    except GateError as error:
        raise GateError(f"{error}\n{result.stdout}{result.stderr}") from error
    print(f"gostyle contexts fixtures: expected exit 3 and {len(expected)} exact diagnostics; valid controls clean")


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
            binary = build_tool(compiler, environment, extension, args.version, temporary, root / "scripts/testdata/gostyle-regression")
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
