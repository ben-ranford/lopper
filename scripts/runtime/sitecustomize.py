"""Lopper Python runtime import capture hook."""

from __future__ import annotations

import builtins
import json
import os
import sys
import threading

try:
    from importlib import metadata as importlib_metadata
except Exception:
    importlib_metadata = None


TRACE_PATH = os.environ.get("LOPPER_RUNTIME_TRACE", "").strip()
ORIGINAL_IMPORT = builtins.__import__
SITE_MARKERS = ("/site-packages/", "/dist-packages/")
WRITE_LOCK = threading.Lock()
STATE = threading.local()
PACKAGE_DISTRIBUTIONS = None


def _entrypoint() -> str:
    if not sys.argv:
        return ""
    entry = sys.argv[0]
    if not entry:
        return ""
    return _abs_path(entry)


def _patched_import(name, globals=None, locals=None, fromlist=(), level=0):
    module = ORIGINAL_IMPORT(name, globals, locals, fromlist, level)
    if TRACE_PATH and level == 0:
        caller = _caller_frame()
        _record_import(name, caller)
        for item in fromlist or ():
            if item == "*":
                continue
            _record_import(f"{name}.{item}", caller)
    return module


def _caller_frame():
    try:
        return sys._getframe(2)
    except ValueError:
        return None


def _record_import(name: str, caller) -> None:
    module_name = (name or "").strip()
    if not module_name:
        return
    module = sys.modules.get(module_name)
    if module is None and "." in module_name:
        module = sys.modules.get(module_name.split(".", 1)[0])
    if module is None:
        return

    resolved = _module_path(module)
    if not _is_third_party_path(resolved):
        return

    event = {
        "language": "python",
        "dependency": _dependency_for_module(module_name),
        "module": module_name,
        "resolved": _abs_path(resolved),
        "parent": _parent_from_frame(caller),
        "entrypoint": ENTRYPOINT,
        "kind": "import",
    }
    _append_event(event)


def _module_path(module) -> str:
    spec = getattr(module, "__spec__", None)
    origin = getattr(spec, "origin", "") if spec is not None else ""
    if not origin or origin in {"built-in", "frozen", "namespace"}:
        origin = getattr(module, "__file__", "")
    return str(origin or "")


def _is_third_party_path(path: str) -> bool:
    normalized = _slash_path(path)
    return any(marker in normalized for marker in SITE_MARKERS)


def _dependency_for_module(module_name: str) -> str:
    top_level = module_name.split(".", 1)[0]
    if not top_level:
        return ""
    distributions = _package_distributions().get(top_level, ())
    if distributions:
        return min(distributions, key=str.lower)
    return top_level


def _package_distributions():
    global PACKAGE_DISTRIBUTIONS
    if PACKAGE_DISTRIBUTIONS is not None:
        return PACKAGE_DISTRIBUTIONS
    PACKAGE_DISTRIBUTIONS = {}
    if importlib_metadata is None or not hasattr(importlib_metadata, "packages_distributions"):
        return PACKAGE_DISTRIBUTIONS
    try:
        PACKAGE_DISTRIBUTIONS = importlib_metadata.packages_distributions()
    except Exception:
        PACKAGE_DISTRIBUTIONS = {}
    return PACKAGE_DISTRIBUTIONS


def _parent_from_frame(frame) -> str:
    if frame is None:
        return ""
    filename = frame.f_globals.get("__file__", "")
    if filename:
        return _abs_path(str(filename))
    module_name = frame.f_globals.get("__name__", "")
    return str(module_name or "")


def _append_event(event) -> None:
    if getattr(STATE, "active", False):
        return
    STATE.active = True
    try:
        parent_dir = os.path.dirname(TRACE_PATH)
        if parent_dir:
            os.makedirs(parent_dir, exist_ok=True)
        payload = json.dumps(event, separators=(",", ":"), sort_keys=True).encode("utf-8") + b"\n"
        flags = os.O_WRONLY | os.O_CREAT | os.O_APPEND
        with WRITE_LOCK:
            fd = os.open(TRACE_PATH, flags, 0o600)
            try:
                os.write(fd, payload)
            finally:
                os.close(fd)
    except Exception:
        return
    finally:
        STATE.active = False


def _abs_path(path: str) -> str:
    if not path:
        return ""
    try:
        return os.path.abspath(path)
    except Exception:
        return path


def _slash_path(path: str) -> str:
    return _abs_path(path).replace(os.sep, "/")


if TRACE_PATH:
    ENTRYPOINT = _entrypoint()
    builtins.__import__ = _patched_import
