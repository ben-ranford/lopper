#!/usr/bin/env python3
"""Read-only permission and owned-transition checks for reviewed Sonar maintenance.

The separately reviewed workflow owns source selection and project serialization.
Candidate source is data. A successful probe is never an analysis certificate.
"""

import datetime
import hashlib
import http.client
import json
import re
import time
import urllib.error
import urllib.parse
import urllib.request

PROJECT = "ben-ranford_lopper"
ORGANIZATION = "ben-ranford"
ORIGIN = "https://sonarcloud.io"
REGEX_KEY = "sonar.branch.longLivedBranches.regex"
AUTOSCAN_KEY = "sonar.autoscan.enabled"
SETTING_KEYS = (REGEX_KEY, AUTOSCAN_KEY)
MAX_BODY = 4 * 1024 * 1024


class MaintenanceError(ValueError):
    """Maintenance cannot establish its next prerequisite; retain pending state."""


class PermissionDenied(MaintenanceError):
    def __init__(self, status):
        super().__init__(f"Sonar endpoint denied access ({status})")
        self.status = status


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class SonarClient:
    def __init__(self, token):
        if not isinstance(token, str) or not token or any(char.isspace() for char in token):
            raise MaintenanceError("A nonempty existing analysis token is required")
        self.token = token
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, endpoint, parameters, *, authenticated=True, write=False):
        allowed = {"components/show", "ce/component", "ce/task", "ce/activity",
                   "settings/values", "settings/set", "settings/reset", "project_branches/list",
                   "project_analyses/search"}
        if endpoint not in allowed or write != (endpoint in ("settings/set", "settings/reset")):
            raise MaintenanceError("Unsupported maintenance endpoint")
        query = urllib.parse.urlencode(parameters).encode("ascii")
        url = ORIGIN + "/api/" + endpoint
        headers = {"Accept": "application/json"}
        if authenticated:
            headers["Authorization"] = "Bearer " + self.token
        if write:
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        else:
            url += "?" + query.decode("ascii")
        request = urllib.request.Request(url, data=query if write else None, headers=headers)
        return self.response(request)

    def response(self, request):
        try:
            with self.opener.open(request, timeout=30) as response:
                content = response.read(MAX_BODY + 1)
        except urllib.error.HTTPError as error:
            # Never echo response bodies, URLs with credentials, or exception text.
            raise PermissionDenied(error.code) from None
        except (OSError, http.client.HTTPException):
            raise MaintenanceError("Sonar request failed; remote outcome is unresolved") from None
        if len(content) > MAX_BODY:
            raise MaintenanceError("Sonar response exceeded its bound")
        if not content:
            return {}
        try:
            return json.loads(content, object_pairs_hook=unique_fields)
        except (ValueError, RecursionError):
            raise MaintenanceError("Malformed Sonar response") from None


def unique_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise MaintenanceError("Duplicate response field")
        result[key] = value
    return result


def require(condition, message):
    if not condition:
        raise MaintenanceError(message)


def component_mapping(client):
    component = client.request("components/show", {"component": PROJECT}).get("component")
    require(isinstance(component, dict) and component.get("key") == PROJECT and
            component.get("organization") == ORGANIZATION and component.get("qualifier") == "TRK",
            "Project mapping does not match the fixed project and organization")
    return {key: component[key] for key in ("key", "organization", "qualifier")}


def task_identity(task):
    require(isinstance(task, dict) and isinstance(task.get("id"), str) and
            re.fullmatch(r"[A-Za-z0-9_-]{1,200}", task["id"]) is not None and
            task.get("type") == "REPORT" and task.get("componentKey") == PROJECT and
            isinstance(task.get("componentId"), str) and bool(task["componentId"]),
            "Task does not identify a fixed-project REPORT")
    require(task.get("organization", ORGANIZATION) == ORGANIZATION,
            "Task belongs to another organization")
    return task["id"], task["componentId"]


def private_read(client, endpoint, parameters):
    try:
        client.request(endpoint, parameters, authenticated=False)
    except PermissionDenied as error:
        require(error.status in (401, 403), "Anonymous permission control is inconclusive")
    else:
        raise MaintenanceError("Public endpoint success cannot establish token permission")
    return client.request(endpoint, parameters)


def probe_permissions(client):
    mapping = component_mapping(client)
    inventory = client.request("ce/component", {"component": PROJECT})
    current = inventory.get("current")
    task_id, component_id = task_identity(current)
    analysis = private_read(client, "ce/task", {"id": task_id}).get("task")
    require(task_identity(analysis) == (task_id, component_id), "Permission task identity changed")
    activity = private_read(client, "ce/activity", {"component": PROJECT, "type": "REPORT", "ps": 1000})
    tasks = activity_tasks(activity)
    require(all(task_identity(task)[1] == component_id for task in tasks), "Administration task project changed")
    require(component_mapping(client) == mapping, "Project mapping changed during permission probe")
    return {"project": PROJECT, "organization": ORGANIZATION, "task": task_id,
            "componentId": component_id, "analysisRead": True, "administrationRead": True,
            "principal": "UNKNOWN", "tokenClass": "UNKNOWN", "settingsCompatibility": "UNPROVEN"}


def activity_tasks(response):
    tasks, paging = response.get("tasks"), response.get("paging")
    require(isinstance(tasks, list) and len(tasks) <= 1000 and isinstance(paging, dict),
            "Missing bounded task inventory")
    require(type(paging.get("total")) is int and paging["total"] == len(tasks) and
            paging.get("pageIndex") == 1 and paging.get("pageSize") == 1000,
            "Incomplete task inventory")
    identities = [task_identity(task)[0] for task in tasks]
    require(len(identities) == len(set(identities)), "Duplicate task identity")
    return tasks


def settings_snapshot(client):
    rows = client.request("settings/values", {"component": PROJECT, "keys": ",".join(SETTING_KEYS)}).get("settings")
    require(isinstance(rows, list) and len(rows) == len(SETTING_KEYS), "Incomplete selected settings")
    result = {}
    for row in rows:
        require(isinstance(row, dict) and row.get("key") in SETTING_KEYS and row["key"] not in result,
                "Unexpected or duplicate selected setting")
        require(isinstance(row.get("value"), str) and type(row.get("inherited", False)) is bool,
                "Malformed selected setting")
        result[row["key"]] = {"value": row["value"], "inherited": row.get("inherited", False)}
    return result


def drain(client, *, timeout=300, clock=time.monotonic, wait=time.sleep):
    deadline = clock() + timeout
    while clock() < deadline:
        response = client.request("ce/activity", {"component": PROJECT, "type": "REPORT",
                                                   "status": "PENDING,IN_PROGRESS", "ps": 1000})
        tasks = activity_tasks(response)
        require(all(task.get("status") in ("PENDING", "IN_PROGRESS") for task in tasks),
                "Unknown active task status")
        require(clock() < deadline, "Task drain deadline expired during inspection")
        if not tasks:
            return
        wait(min(1, max(0, deadline - clock())))
    raise MaintenanceError("Task drainage remains unresolved; do not submit or restore Autoscan")


def owned_write(client, state, key, value, save):
    require(settings_snapshot(client) == state["expected"], "Settings changed before owned write")
    state["unresolvedWrite"] = {"key": key, "value": value}
    save(state)
    client.request("settings/set", {"component": PROJECT, "key": key, "value": value}, write=True)
    # Record attempted ownership before reread: a lost response is never rollback permission.
    expected = dict(state["expected"])
    expected[key] = {"value": value, "inherited": False}
    state["expected"] = expected
    state["changed"].append(key)
    save(state)
    require(settings_snapshot(client) == expected, "Settings changed during owned write")
    state["unresolvedWrite"] = None
    save(state)


def verified_scope(encoded, expected_digest):
    require(isinstance(encoded, bytes) and len(encoded) <= MAX_BODY and
            isinstance(expected_digest, str) and re.fullmatch(r"[0-9a-f]{64}", expected_digest),
            "Reviewed scanner scope binding is missing")
    require(hashlib.sha256(encoded).hexdigest() == expected_digest, "Scanner scope bytes changed")
    scope = json.loads(encoded, object_pairs_hook=unique_fields)
    require(isinstance(scope, dict) and set(scope) == {"project", "organization", "properties", "inventory"} and
            scope["project"] == PROJECT and scope["organization"] == ORGANIZATION,
            "Scanner scope belongs to another project")
    inventory = scope["inventory"]
    require(isinstance(inventory, dict) and set(inventory) == {
        "sources", "tests", "coverage", "qualityProfiles", "qualityGate"} and
        all(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) for value in inventory.values()),
        "Complete reviewed source, test, coverage and quality inventory is required")
    properties = scope["properties"]
    require(isinstance(properties, dict) and bool(properties), "Reviewed scanner properties are missing")
    for key, value in properties.items():
        require(isinstance(key, str) and re.fullmatch(r"sonar\.[A-Za-z0-9_.]+", key) and
                isinstance(value, str) and not any(char in value for char in "\r\n\0"),
                "Unsafe scanner property")
    allowed = {"sonar.sources", "sonar.tests", "sonar.exclusions", "sonar.inclusions",
               "sonar.test.inclusions", "sonar.test.exclusions", "sonar.coverage.exclusions",
               "sonar.cpd.exclusions", "sonar.sourceEncoding", "sonar.go.coverage.reportPaths",
               "sonar.javascript.lcov.reportPaths", "sonar.python.coverage.reportPaths",
               "sonar.coverageReportPaths", "sonar.testExecutionReportPaths"}
    require(set(properties) <= allowed, "Unreviewed scanner runtime or scope property")
    return scope


def configure(client, reference, scope_bytes, scope_digest, *, save):
    verified_scope(scope_bytes, scope_digest)
    require(isinstance(reference, str) and re.fullmatch(r"codex/v1\.8\.10-[A-Za-z0-9_./-]+", reference),
            "Target is outside the reviewed batch")
    permissions = probe_permissions(client)
    before = settings_snapshot(client)
    require(before[AUTOSCAN_KEY]["value"] == "true", "Unexpected initial analysis method")
    state = {"before": before, "expected": before, "changed": [], "permissions": permissions,
             "submissionUnresolved": False, "unresolvedWrite": None, "scopeDigest": scope_digest}
    save(state)
    escaped = re.escape(reference)
    current = before[REGEX_KEY]["value"]
    if re.fullmatch(current, reference) is None:
        owned_write(client, state, REGEX_KEY, "(?:" + current + ")|" + escaped, save)
    owned_write(client, state, AUTOSCAN_KEY, "false", save)
    drain(client)
    return state


def restore(client, state, *, save):
    require(state.get("unresolvedWrite") is None, "A settings write remains unresolved")
    require(state.get("submissionUnresolved") is False, "A submission may still be running")
    drain(client)
    for key in list(state["changed"]):
        if key == AUTOSCAN_KEY:
            drain(client)
        require(settings_snapshot(client) == state["expected"], "External settings change prevents restoration")
        previous = state["before"][key]
        endpoint = "settings/reset" if previous["inherited"] else "settings/set"
        parameters = {"component": PROJECT, "key": key}
        if not previous["inherited"]:
            parameters["value"] = previous["value"]
        state["unresolvedWrite"] = {"key": key, "restore": previous}
        save(state)
        client.request(endpoint, parameters, write=True)
        state["expected"][key] = previous
        save(state)
        require(settings_snapshot(client) == state["expected"], "Settings changed during restoration")
        state["unresolvedWrite"] = None
        state["changed"].remove(key)
        save(state)


def validate_parent_history(analyses):
    identities = set()
    previous = None
    for row in analyses:
        identity, date = row.get("key"), row.get("date")
        require(isinstance(identity, str) and identity and identity not in identities,
                "Duplicate or missing parent analysis identity")
        require(isinstance(date, str) and re.fullmatch(
            r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})", date),
            "Invalid parent completion time")
        try:
            instant = datetime.datetime.fromisoformat(date.replace("Z", "+00:00"))
        except ValueError:
            raise MaintenanceError("Invalid parent completion time") from None
        require(previous is None or previous > instant, "Parent history is unordered or ambiguous")
        identities.add(identity)
        previous = instant


def parent_baseline(branches, history, reference, revision):
    rows = branches.get("branches")
    require(isinstance(rows, list) and len(rows) <= 1000, "Missing bounded branch inventory")
    selected = [row for row in rows if row.get("name") == reference]
    require(len(selected) == 1 and selected[0].get("type") == "LONG", "Actual target lacks an independent LONG baseline")
    analyses = history.get("analyses")
    paging = history.get("paging")
    require(isinstance(analyses, list) and len(analyses) <= 1000 and isinstance(paging, dict) and
            paging.get("total") == len(analyses) and paging.get("pageIndex") == 1,
            "Incomplete parent analysis history")
    require(bool(analyses) and analyses[0].get("revision") == revision and
            isinstance(analyses[0].get("key"), str) and bool(analyses[0]["key"]),
            "Latest parent analysis does not match the actual committed base")
    validate_parent_history(analyses)
    return {"branch": reference, "revision": revision, "analysisId": analyses[0]["key"],
            "date": analyses[0]["date"]}


def git_read(root, *arguments):
    import os
    import subprocess
    environment = {"PATH": "/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_SYSTEM": os.devnull,
                   "GIT_CONFIG_GLOBAL": os.devnull, "GIT_NO_REPLACE_OBJECTS": "1"}
    result = subprocess.run(["/usr/bin/git", "-c", "core.hooksPath=" + os.devnull,
                             "-c", "core.fsmonitor=false", *arguments], cwd=root, env=environment,
                            capture_output=True, text=True, check=False, timeout=30)
    require(result.returncode == 0, "Immutable Git source validation failed")
    return result.stdout.strip()


def verify_checkout(root, revision):
    from pathlib import Path
    require(isinstance(revision, str) and re.fullmatch(r"[a-f0-9]{40}", revision), "Full immutable source SHA required")
    root = Path(root)
    require(not root.is_symlink() and root.resolve(strict=True) == root.absolute(), "Source root must be canonical")
    require(git_read(root, "rev-parse", "HEAD") == revision, "Source checkout revision changed")
    require(git_read(root, "status", "--porcelain=v1", "--untracked-files=all") == "", "Source checkout is not clean")
    flags = git_read(root, "ls-files", "-v", "-z")
    require(all(row.startswith("H ") for row in flags.split("\0") if row), "Source index hides tracked content")
    return root


def scan_properties(scope, *, reference, revision, pull_number=None, head_ref=None):
    require(isinstance(revision, str) and re.fullmatch(r"[a-f0-9]{40}", revision), "Actual revision is missing")
    require(isinstance(reference, str) and re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_./-]*", reference), "Unsafe target ref")
    properties = dict(scope["properties"])
    properties.update({"sonar.host.url": ORIGIN, "sonar.projectKey": PROJECT, "sonar.organization": ORGANIZATION})
    if pull_number is None:
        properties["sonar.branch.name"] = reference
    else:
        require(type(pull_number) is int and pull_number > 0 and isinstance(head_ref, str) and
                re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_./-]*", head_ref), "Unsafe actual PR identity")
        properties.update({"sonar.pullrequest.key": str(pull_number), "sonar.pullrequest.branch": head_ref,
                           "sonar.pullrequest.base": reference})
    return properties


def scanner_archive(archive, destination, manifest):
    import os
    from pathlib import Path
    import stat
    import zipfile
    archive, destination = Path(archive), Path(destination)
    require(not archive.is_symlink() and archive.is_file() and archive.stat().st_size <= 200 * 1024 * 1024,
            "Scanner archive is missing or oversized")
    with archive.open("rb") as source:
        digest = hashlib.sha256()
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    require(digest.hexdigest() == manifest["sha256"], "Official scanner archive digest mismatch")
    destination.mkdir(mode=0o700)
    with zipfile.ZipFile(archive) as zipped:
        entries = zipped.infolist()
        require(len(entries) <= 20000 and sum(row.file_size for row in entries) <= 1024 * 1024 * 1024,
                "Scanner archive exceeds its extraction bound")
        seen = set()
        for entry in entries:
            name = Path(entry.filename)
            mode = entry.external_attr >> 16
            require(not name.is_absolute() and ".." not in name.parts and "\\" not in entry.filename and
                    entry.filename not in seen and not stat.S_ISLNK(mode), "Unsafe scanner archive member")
            seen.add(entry.filename)
            path = destination / name
            if entry.is_dir():
                path.mkdir(parents=True, exist_ok=True)
            else:
                path.parent.mkdir(parents=True, exist_ok=True)
                with zipped.open(entry) as source, path.open("xb") as target:
                    for chunk in iter(lambda: source.read(1024 * 1024), b""):
                        target.write(chunk)
                os.chmod(path, 0o700 if mode & 0o111 else 0o600)
    executable = destination / ("sonar-scanner-" + manifest["version"] + "-linux-x64") / "bin/sonar-scanner"
    require(executable.is_file() and not executable.is_symlink(), "Official scanner executable missing")
    return executable


def run_scanner(executable, source, properties, workspace, token, *, timeout=600):
    import os
    import signal
    import subprocess
    from pathlib import Path
    workspace = Path(workspace)
    configuration = workspace / "scanner.properties"
    metadata = workspace / "report-task.txt"
    properties = dict(properties, **{"sonar.scanner.metadataFilePath": str(metadata),
                                   "sonar.working.directory": str(workspace / "work")})
    configuration.write_text("".join(key + "=" + value + "\n" for key, value in sorted(properties.items())))
    os.chmod(configuration, 0o600)
    environment = {"PATH": "/usr/bin:/bin", "HOME": str(workspace), "SONAR_USER_HOME": str(workspace / "home"),
                   "SONAR_TOKEN": token, "LANG": "C.UTF-8"}
    command = [str(executable), "-Dproject.settings=" + str(configuration)]
    process = subprocess.Popen(command, cwd=source, env=environment, stdin=subprocess.DEVNULL,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    try:
        result = process.wait(timeout=timeout)
    except (subprocess.TimeoutExpired, OSError):
        stop_scanner(process, signal.SIGTERM, signal.SIGKILL)
        raise MaintenanceError("Scanner wait failed; server submission remains unresolved") from None
    require(result == 0, "Scanner failed; server submission remains unresolved")
    require(not metadata.is_symlink() and metadata.is_file() and metadata.stat().st_size <= 16384,
            "Scanner task identity is missing; submission remains unresolved")
    pairs = []
    for line in metadata.read_text().splitlines():
        require("=" in line, "Malformed scanner task identity")
        pairs.append(line.split("=", 1))
    report = unique_fields(pairs)
    require(report.get("projectKey") == PROJECT and report.get("serverUrl") == ORIGIN,
            "Scanner report belongs to another project or server")
    task_id = report.get("ceTaskId")
    require(isinstance(task_id, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,200}", task_id), "Missing server task ID")
    return task_id


def stop_scanner(process, term, kill):
    import os
    import subprocess
    errors = []
    try:
        os.killpg(process.pid, term)
    except OSError as error:
        errors.append(type(error).__name__)
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, kill)
        except OSError as error:
            errors.append(type(error).__name__)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            errors.append("unjoined scanner")
    require(not errors, "Scanner cleanup could not be verified; retain unresolved submission")


def completed_task(client, task_id, *, timeout=300, clock=time.monotonic, wait=time.sleep):
    deadline = clock() + timeout
    while clock() < deadline:
        task = client.request("ce/task", {"id": task_id}).get("task")
        require(task_identity(task)[0] == task_id, "Submitted task identity changed")
        require(clock() < deadline, "Task deadline expired during inspection")
        if task.get("status") == "SUCCESS":
            require(isinstance(task.get("analysisId"), str) and bool(task["analysisId"]), "Completed task lacks analysis identity")
            return task
        require(task.get("status") in ("PENDING", "IN_PROGRESS"), "Submitted task failed or has unknown status")
        wait(min(1, max(0, deadline - clock())))
    raise MaintenanceError("Submitted task remains unresolved; retain disabled Autoscan")


def verify_bootstrap(context, expected_workflow, expected_ref, driver_root, driver_revision):
    import os
    require("SONAR_TOKEN" not in os.environ, "Driver bootstrap must precede secret introduction")
    require(context.get("event") == "workflow_dispatch" and context.get("repository") == "ben-ranford/lopper" and
            context.get("repositoryId") == 1155023607 and context.get("ref") == expected_ref,
            "Unexpected maintenance dispatch context")
    require(isinstance(expected_workflow, str) and re.fullmatch(r"[a-f0-9]{40}", expected_workflow) and
            context.get("workflowSHA") == expected_workflow, "Workflow source changed from the independent review binding")
    require(type(context.get("runId")) is int and context["runId"] > 0 and
            type(context.get("runAttempt")) is int and context["runAttempt"] > 0, "Missing dispatch generation")
    root = verify_checkout(driver_root, driver_revision)
    origin = git_read(root, "remote", "get-url", "--all", "origin")
    require(origin in ("https://github.com/ben-ranford/lopper", "https://github.com/ben-ranford/lopper.git"),
            "Driver checkout has another origin")
    return {"workflow": expected_workflow, "driver": driver_revision, "ref": expected_ref,
            "runId": context["runId"], "runAttempt": context["runAttempt"]}


def current_parent(client, pair):
    return parent_baseline(client.request("project_branches/list", {"project": PROJECT}),
                           client.request("project_analyses/search", {"project": PROJECT,
                                                                     "branch": pair["base_ref"], "p": 1, "ps": 1000}),
                           pair["base_ref"], pair["base"])


def submit_analysis(client, pair, state, source, archive, workspace, scope_bytes, scope_digest,
                    *, is_child, head_ref, save, live_pair, manifest, scanner=run_scanner):
    require(state.get("unresolvedWrite") is None and state.get("submissionUnresolved") is False,
            "Earlier transition or submission is unresolved")
    require(state.get("scopeDigest") == scope_digest, "Scope differs from the configured operation")
    scope = verified_scope(scope_bytes, scope_digest)
    require(live_pair() == pair, "Actual base/head pair changed before analysis")
    require(settings_snapshot(client) == state["expected"] and
            state["expected"][AUTOSCAN_KEY]["value"] == "false", "Analysis method or owned settings changed")
    drain(client)
    baseline = current_parent(client, pair) if is_child else None
    revision = pair["head"] if is_child else pair["base"]
    source = verify_checkout(source, revision)
    require(git_read(source, "rev-parse", "refs/remotes/origin/" + pair["base_ref"]) == pair["base"],
            "Fetched actual target revision changed")
    executable = scanner_archive(archive, workspace / "scanner", manifest)
    properties = scan_properties(scope, reference=pair["base_ref"], revision=revision,
                                 pull_number=pair["pull_number"] if is_child else None, head_ref=head_ref)
    state["submissionUnresolved"] = True
    save(state)
    task_id = scanner(executable, source, properties, workspace, client.token)
    state["taskId"] = task_id
    save(state)
    task = completed_task(client, task_id)
    require(live_pair() == pair, "Actual base/head pair changed during analysis")
    verify_checkout(source, revision)
    if is_child:
        require(current_parent(client, pair) == baseline, "Parent baseline changed during child analysis")
    else:
        baseline = current_parent(client, pair)
        require(baseline["analysisId"] == task["analysisId"], "Completed baseline is not this actual-base task")
    state["submissionUnresolved"] = False
    state["analysisId"] = task["analysisId"]
    save(state)
    # Child certification still requires sonar_maintenance_verify.js. A scanner
    # exit or CE task success is deliberately not a clean-analysis certificate.
    return {"task": task_id, "analysisId": task["analysisId"], "revision": revision,
            "parent": baseline, "certification": "PENDING_STRICT_VERIFIER"}
