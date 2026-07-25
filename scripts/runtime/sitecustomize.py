"""Lopper Python runtime import capture hook."""

from __future__ import annotations

import builtins
import importlib.machinery
import importlib.util
import json
import os
import sys
import threading

try:
    from importlib import metadata as importlib_metadata
except Exception:
    importlib_metadata = None


TRACE_PATH = os.environ.get("LOPPER_RUNTIME_TRACE", "").strip()
REPO_ROOT = os.environ.get("LOPPER_RUNTIME_REPO_ROOT", "").strip()
ORIGINAL_IMPORT = builtins.__import__
SITE_MARKERS = ("/site-packages/", "/dist-packages/")
WRITE_LOCK = threading.Lock()
STATE = threading.local()
PACKAGE_DISTRIBUTIONS = None
TRUSTED_REPO_ROOTS = ()


def _entrypoint() -> str:
    if not sys.argv:
        return ""
    entry = sys.argv[0]
    if not entry:
        return ""
    return _normalize_repo_context(entry)


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
    module_name = _module_identifier(name)
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
        "dependency": _dependency_identifier(_dependency_for_module(module_name)),
        "module": module_name,
        "resolved": module_name,
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


def _module_identifier(value: str) -> str:
    candidate = (value or "").strip()
    if not candidate:
        return ""
    parts = candidate.split(".")
    if any(not part or not part.isidentifier() for part in parts):
        return ""
    return ".".join(parts)


def _dependency_identifier(value: str) -> str:
    candidate = (value or "").strip()
    if candidate in {"", ".", ".."}:
        return ""
    if any(not (character.isalnum() or character in "._-") for character in candidate):
        return ""
    return candidate


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
        return _normalize_repo_context(str(filename))
    module_name = frame.f_globals.get("__name__", "")
    return _module_identifier(str(module_name or ""))


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


def _lexical_path(path: str) -> str:
    if not path:
        return ""
    try:
        return os.path.normcase(os.path.normpath(os.path.abspath(path)))
    except Exception:
        return ""


def _lexical_candidate_path(path: str, repo_root: str) -> str:
    if not path:
        return ""
    if os.path.isabs(path) or _is_native_windows_absolute(path):
        return _lexical_path(path)
    return _lexical_path(os.path.join(repo_root, path))


def _trusted_repo_roots(repo_root: str):
    original = _lexical_path(repo_root)
    if not original:
        return ()
    try:
        resolved = _real_path(original)
    except Exception:
        resolved = ""
    return tuple(dict.fromkeys(root for root in (original, resolved) if root))


def _repo_relative_under_trusted_roots(path: str) -> str:
    for root in TRUSTED_REPO_ROOTS:
        try:
            relative = os.path.relpath(path, root)
        except (OSError, ValueError):
            continue
        if not _repo_relative_starts_outside(relative):
            return relative
    return ""


def _normalize_repo_context(path: str) -> str:
    candidate = (path or "").strip()
    if not candidate:
        return ""
    if not TRUSTED_REPO_ROOTS:
        return ""
    if _rejects_non_native_path(candidate):
        return ""
    lexical_candidate = _lexical_candidate_path(candidate, TRUSTED_REPO_ROOTS[0])
    if not lexical_candidate:
        return ""
    if not _repo_relative_under_trusted_roots(lexical_candidate):
        return ""
    try:
        resolved = _real_path(lexical_candidate)
    except Exception:
        return ""
    relative = _repo_relative_under_trusted_roots(resolved)
    if not relative:
        return ""
    return relative.replace(os.sep, "/")


def _rejects_non_native_path(value: str) -> bool:
    if "\0" in value:
        return True
    if _has_path_scheme(value) and not _is_native_windows_absolute(value):
        return True
    if os.name != "nt" and (_is_windows_absolute(value) or _has_unc_prefix(value)):
        return True
    return os.name != "nt" and "\\" in value


def _has_path_scheme(value: str) -> bool:
    separator = value.find(":")
    if separator <= 0 or not value[0].isalpha():
        return False
    return all(character.isalnum() or character in "+.-" for character in value[1:separator])


def _is_native_windows_absolute(value: str) -> bool:
    return os.name == "nt" and (_is_windows_absolute(value) or _has_unc_prefix(value))


def _is_windows_absolute(value: str) -> bool:
    return (
        len(value) >= 3
        and value[0].isalpha()
        and value[1] == ":"
        and value[2] in "\\/"
    )


def _has_unc_prefix(value: str) -> bool:
    return len(value) >= 2 and value[0] in "\\/" and value[1] in "\\/"


def _repo_relative_starts_outside(value: str) -> bool:
    return value in {".", ".."} or value.startswith(".." + os.sep)


def _chain_project_sitecustomize() -> None:
    hook_dir = _real_path(os.path.dirname(__file__))
    search_path = [entry for entry in sys.path if _real_path(entry) != hook_dir]
    spec = importlib.machinery.PathFinder.find_spec("sitecustomize", search_path)
    if spec is None or spec.loader is None:
        return
    if _real_path(getattr(spec, "origin", "")) == _real_path(__file__):
        return
    project_hook = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(project_hook)


def _real_path(path: str) -> str:
    candidate = path or os.getcwd()
    return os.path.normcase(os.path.realpath(os.path.abspath(candidate)))


TRUSTED_REPO_ROOTS = _trusted_repo_roots(REPO_ROOT)


if TRACE_PATH:
    ENTRYPOINT = _entrypoint()
    _chain_project_sitecustomize()
    ORIGINAL_IMPORT = builtins.__import__
    builtins.__import__ = _patched_import
