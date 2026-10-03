#!/usr/bin/env python3
"""Protected reuse-check event controller; candidate trees are data, never code."""

import argparse
import base64
from datetime import datetime
import http.client
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile
import zlib

import reuse_policy_review as policy


class EventError(ValueError):
    """The event or live evidence does not establish a passing result."""


class InactivePull(EventError):
    """A listed pull request is no longer open against the default branch."""


class CIDeferred(EventError):
    """Authenticated CI has not supplied a current proof yet; never approval."""


SNAPSHOT_KEYS = frozenset(("version", "repository", "repository_id", "head_repository_id",
                           "pull_number", "base", "head", "base_ref"))
RESULT_KEYS = frozenset(("version", "snapshot", "candidate", "detector_exit", "policy_paths"))
SUPPRESSION_KEYS = frozenset(("headSHA", "baseSHA", "runId", "runAttempt", "artifactId", "suppressionCount"))
DEFERRED_KEYS = frozenset(("version", "snapshot", "reason", "runId", "runAttempt"))
CI_WORKFLOW = ".github/workflows/ci.yml"
CI_NONTERMINAL = frozenset(("queued", "in_progress", "waiting", "pending", "requested"))
CONTEXT = "reuse-check"


def number(value):
    if type(value) is not int or value < 1:
        raise EventError("Expected a positive integer identifier")
    return value


def repository_name(value):
    policy.repository_name(value)
    if any(part in (".", "..") for part in value.split("/")):
        raise EventError("Invalid repository name")
    return value


def validate_snapshot(snapshot):
    if not isinstance(snapshot, dict) or set(snapshot) != SNAPSHOT_KEYS or snapshot["version"] != 1:
        raise EventError("Invalid event snapshot schema")
    if type(snapshot["version"]) is not int:
        raise EventError("Invalid event snapshot version")
    repository_name(snapshot["repository"])
    for key in ("repository_id", "head_repository_id", "pull_number"):
        number(snapshot[key])
    for key in ("base", "head"):
        policy.immutable_sha(snapshot[key])
    if not isinstance(snapshot["base_ref"], str) or not snapshot["base_ref"]:
        raise EventError("Missing protected target branch")
    return snapshot


def merge_group_pair(event):
    """Parse immutable queue revisions for tests; queue dispatch is not enabled."""
    group = event["merge_group"]
    return policy.immutable_sha(group["base_sha"]), policy.immutable_sha(group["head_sha"])


def api_segments(segments):
    decoded = [urllib.parse.unquote(segment) for segment in segments]
    for segment in decoded:
        if (not segment or any(part in ("", ".", "..") for part in segment.split("/"))
                or "\\" in segment or "%" in segment
                or any(ord(char) < 32 or ord(char) == 127 for char in segment)):
            raise EventError("Unsafe GitHub API segment")
    return [urllib.parse.quote(segment, safe="") for segment in decoded]


def api_query(raw):
    if not raw:
        return ""
    try:
        query = urllib.parse.parse_qs(raw, keep_blank_values=True, strict_parsing=True)
    except ValueError as error:
        raise EventError("Malformed GitHub API query") from error
    if (set(query) not in ({"per_page", "page"}, {"per_page", "page", "head_sha", "event"})
            or query["per_page"] != ["100"]
            or len(query["page"]) != 1
            or not re.fullmatch(r"[1-9]\d{0,2}", query["page"][0], flags=re.ASCII)):
        raise EventError("Unsupported GitHub API query")
    if "head_sha" in query:
        if len(query["head_sha"]) != 1 or query["event"] != ["pull_request"]:
            raise EventError("Unsupported CI run query")
        policy.immutable_sha(query["head_sha"][0])
    return urllib.parse.urlencode(query, doseq=True)


def github_api_url(path):
    """Build one origin URL from encoded segments, never concatenate an API locator."""
    if not isinstance(path, str) or any(ord(char) < 32 or ord(char) == 127 for char in path):
        raise EventError("Invalid GitHub API path")
    parsed = urllib.parse.urlsplit(path)
    segments = parsed.path.split("/")
    if (parsed.scheme or parsed.netloc or parsed.fragment or len(segments) < 4
            or segments[:2] != ["", "repos"]):
        raise EventError("Invalid GitHub API path")
    try:
        repository_name("/".join(segments[2:4]))
    except ValueError as error:
        raise EventError("Invalid API repository") from error
    normalized = "/" + "/".join(api_segments(segments[1:]))
    query = api_query(parsed.query)
    if "head_sha=" in query and segments[4:] != ["actions", "workflows", "ci.yml", "runs"]:
        raise EventError("CI run filters require the fixed CI workflow endpoint")
    return urllib.parse.urlunsplit(("https", "api.github.com", normalized, query, ""))


def evidence_path(requested, name):
    """Limit CLI evidence reads/writes to fixed files in runner scratch."""
    filenames = {"snapshot": "reuse-snapshot.json", "result": "reuse-result.json",
                 "suppression": "reuse-suppression.json", "deferred": "reuse-deferred.json"}
    if name not in filenames:
        raise EventError("Unsupported evidence file")
    directory = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
    expected = directory / filenames[name]
    if expected.is_symlink() or Path(requested).resolve() != expected:
        raise EventError("Evidence must use its fixed non-symlink runner scratch path")
    return expected


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class GitHub:
    def __init__(self, token):
        if not isinstance(token, str) or not token or any(char.isspace() for char in token):
            raise EventError("An explicit GitHub job token is required")
        self.token = token
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, path, data=None):
        request = urllib.request.Request(
            github_api_url(path),
            data=None if data is None else json.dumps(data).encode(),
            headers={"Authorization": "Bearer " + self.token,
                     "Accept": "application/vnd.github+json",
                     "X-GitHub-Api-Version": "2022-11-28",
                     "Content-Type": "application/json"})
        try:
            with self.opener.open(request, timeout=30) as response:
                content = response.read(16 * 1024 * 1024 + 1)
        except (OSError, http.client.HTTPException) as error:
            raise EventError("GitHub API request failed") from error
        if len(content) > 16 * 1024 * 1024:
            raise EventError("GitHub API response exceeded its safety bound")
        return json.loads(content)

    def pages(self, path):
        if "?" in path:
            raise EventError("Pagination requires a fixed endpoint")
        items = []
        for page in range(1, 101):
            batch = self.request(f"{path}?per_page=100&page={page}")
            if not isinstance(batch, list) or len(batch) > 100:
                raise EventError("Malformed GitHub pagination response")
            items.extend(batch)
            if len(batch) < 100:
                return items
        raise EventError("GitHub pagination did not finish within its safety bound")

    def artifact_archive(self, repository, artifact_id):
        path = f"/repos/{repository_name(repository)}/actions/artifacts/{number(artifact_id)}/zip"
        request = urllib.request.Request("https://api.github.com" + path,
                                         headers={"Authorization": "Bearer " + self.token})
        try:
            with self.opener.open(request, timeout=30):
                raise EventError("Expected an authenticated artifact download redirect")
        except urllib.error.HTTPError as error:
            if error.code != 302:
                raise EventError("Artifact download authorization failed") from error
            location = artifact_location(error.headers.get("Location", ""))
        except (OSError, http.client.HTTPException) as error:
            raise EventError("Artifact download authorization failed") from error
        # Never forward API authorization to the separate signed storage URL.
        try:
            with self.opener.open(urllib.request.Request(location), timeout=30) as response:
                archive = response.read(65537)
        except (OSError, http.client.HTTPException) as error:
            raise EventError("Artifact storage download failed") from error
        if len(archive) > 65536:
            raise EventError("Review wakeup archive exceeded its safety bound")
        return archive


def artifact_location(location):
    parsed = urllib.parse.urlsplit(location)
    host = parsed.hostname or ""
    if (parsed.scheme != "https" or parsed.username or parsed.password or parsed.fragment
            or parsed.port not in (None, 443)
            or not host.endswith((".blob.core.windows.net", ".githubusercontent.com"))):
        raise EventError("Artifact redirect is not a supported credential-free HTTPS storage URL")
    return location


def artifact_pull_number(archive):
    try:
        contents = wakeup_contents(archive)
    except (zipfile.BadZipFile, RuntimeError, EOFError, zlib.error) as error:
        raise EventError("Review wakeup archive could not be decoded safely") from error
    document = json.loads(contents.decode("utf-8"), object_pairs_hook=policy.unique_object)
    if not isinstance(document, dict) or set(document) != {"pull_number"}:
        raise EventError("Review wakeup JSON must contain only a PR number")
    return number(document["pull_number"])


def wakeup_contents(archive):
    with zipfile.ZipFile(io.BytesIO(archive)) as zipped:
        entries = zipped.infolist()
        if len(entries) != 1:
            raise EventError("Review wakeup archive must contain exactly one file")
        entry = entries[0]
        mode = (entry.external_attr >> 16) & 0o170000
        if (entry.filename != "reuse-review-wakeup.json" or entry.is_dir()
                or mode not in (0, 0o100000) or entry.file_size > 1024
                or entry.flag_bits & ~0x80E
                or entry.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)):
            raise EventError("Review wakeup file metadata is unsafe")
        with zipped.open(entry) as stream:
            contents = stream.read(1025)
        if len(contents) > 1024:
            raise EventError("Review wakeup JSON exceeded its safety bound")
        return contents


def signal_artifact(api, repository, run):
    expected = f"reuse-review-wakeup-{number(run['id'])}-{number(run['run_attempt'])}"
    matches = []
    for page in range(1, 101):
        response = api.request(f"/repos/{repository}/actions/runs/{run['id']}/artifacts?per_page=100&page={page}")
        artifacts = response["artifacts"]
        if not isinstance(artifacts, list) or len(artifacts) > 100:
            raise EventError("Malformed artifact listing")
        matches.extend(artifact for artifact in artifacts if artifact["name"] == expected)
        if len(artifacts) < 100:
            break
    else:
        raise EventError("Artifact listing exceeded its safety bound")
    if len(matches) != 1 or matches[0]["expired"] is not False:
        raise EventError("Current run attempt has no unique unexpired review wakeup artifact")
    artifact = matches[0]
    if type(artifact["size_in_bytes"]) is not int or not 0 < artifact["size_in_bytes"] <= 65536:
        raise EventError("Review wakeup artifact exceeded its safety bound")
    return artifact_pull_number(api.artifact_archive(repository, number(artifact["id"])))


def live_repository(api, repository, identifier):
    repository_name(repository)
    document = api.request(f"/repos/{repository}")
    if (document["id"] != number(identifier)
            or document["full_name"].casefold() != repository.casefold()):
        raise EventError("Repository identity changed")
    return document


def pull_snapshot(api, repository, identifier, pull_number, *, require_current_base=True):
    document = live_repository(api, repository, identifier)
    pull = api.request(f"/repos/{repository}/pulls/{number(pull_number)}")
    if (pull["number"] != pull_number or pull["base"]["repo"]["id"] != identifier
            or pull["base"]["repo"]["full_name"].casefold() != repository.casefold()):
        raise EventError("Live pull request repository or number does not match")
    if pull["state"] != "open" or pull["base"]["ref"] != document["default_branch"]:
        raise InactivePull("Pull request is not open against this repository's default branch")
    if require_current_base:
        branch = urllib.parse.quote(document["default_branch"], safe="")
        target = api.request(f"/repos/{repository}/git/ref/heads/{branch}")
        if policy.immutable_sha(target["object"]["sha"]) != pull["base"]["sha"]:
            raise EventError("Pull request base does not match the current protected target")
    snapshot = validate_snapshot({
        "version": 1, "repository": repository, "repository_id": identifier,
        "head_repository_id": pull["head"]["repo"]["id"], "pull_number": pull_number,
        "base": pull["base"]["sha"], "head": pull["head"]["sha"], "base_ref": pull["base"]["ref"]})
    return snapshot


def same_live_pair(api, snapshot):
    if pull_snapshot(api, snapshot["repository"], snapshot["repository_id"],
                     snapshot["pull_number"]) != snapshot:
        raise EventError("Live pull request base or head changed; rerun required")


def signal_pull_number(api, event, repository, identifier, workflow, actor):
    run = api.request(f"/repos/{repository}/actions/runs/{number(event['workflow_run']['id'])}")
    if (event["action"] != "completed" or run["status"] != "completed"
            or run["event"] != "pull_request_review" or run["path"] != workflow
            or run["repository"]["id"] != identifier or run["id"] != event["workflow_run"]["id"]
            or run["name"] != "Reuse review signal"):
        raise EventError("Review signal does not match the trusted workflow and repository")
    owner_wakeup = run["actor"]["login"] == actor
    try:
        pull_number = resolve_signal_pull(api, repository, identifier, run)
    except (OSError, ValueError, KeyError, TypeError, zipfile.BadZipFile) as error:
        document = live_repository(api, repository, identifier)
        invalidate_pulls(api, repository, identifier, document["default_branch"],
                         "Review wakeup unresolved; refresh exact-revision reuse analysis",
                         dismissed_reviewer=None if owner_wakeup else actor)
        raise EventError("Authenticated review wakeup could not resolve a PR; "
                         "eligible open target PRs invalidated pending explicit refresh") from error
    # A different actor can dismiss the owner's review. Only live revocation,
    # never the signal artifact or another reviewer's decision, permits this
    # wakeup. Rechecking state also makes older dismissal events harmless after
    # a newer owner review and does not depend on the dismisser retaining access.
    if not owner_wakeup and not owner_review_dismissed(api, repository, pull_number, actor):
        raise EventError("Non-owner review wakeup has no live owner dismissal")
    return pull_number


def owner_review_dismissed(api, repository, pull_number, actor):
    reviewers = policy.trusted_reviewers([actor])
    reviews = api.pages(f"/repos/{repository}/pulls/{number(pull_number)}/reviews")
    expected_url = f"https://api.github.com/repos/{repository}/pulls/{pull_number}"
    if not any(policy.authorized_submission(review, policy.review_identity(review)[1],
                                            reviewers, expected_url) is not None for review in reviews):
        return False
    review = policy.latest_authorized_review(reviews, reviewers, expected_url)
    return review["state"] == "DISMISSED"


def ci_workflow(api, repository):
    workflow = api.request(f"/repos/{repository}/actions/workflows/ci.yml")
    if workflow["path"] != CI_WORKFLOW or workflow["name"] != "ci":
        raise EventError("CI workflow identity changed")
    number(workflow["id"])
    return workflow


def validate_ci_identity(run, workflow, repository_id):
    if (run["workflow_id"] != workflow["id"] or run["path"] != CI_WORKFLOW
            or run["name"] != "ci" or run["event"] != "pull_request"
            or run["repository"]["id"] != repository_id):
        raise EventError("CI producer does not match the trusted workflow and repository")
    number(run["id"])
    number(run["run_attempt"])
    number(run["head_repository"]["id"])
    policy.immutable_sha(run["head_sha"])


def exact_ci_association(run, snapshot):
    associations = run["pull_requests"]
    if not isinstance(associations, list):
        raise EventError("Malformed CI pull request associations")
    matches = [pull for pull in associations if pull["number"] == snapshot["pull_number"]]
    if len(matches) != 1:
        raise EventError("CI producer has no unique pull request association")
    pull = matches[0]
    if (pull["head"]["sha"] != snapshot["head"] or pull["base"]["sha"] != snapshot["base"]
            or pull["base"]["ref"] != snapshot["base_ref"]
            or pull["head"]["repo"]["id"] != snapshot["head_repository_id"]
            or pull["base"]["repo"]["id"] != snapshot["repository_id"]
            or run["head_sha"] != snapshot["head"]
            or run["head_repository"]["id"] != snapshot["head_repository_id"]):
        raise EventError("CI producer association does not match the exact current pull request")


def ci_pull_number(api, event, repository, identifier):
    if event["action"] not in ("requested", "in_progress", "completed"):
        raise EventError("Unsupported CI wakeup action")
    run_id = number(event["workflow_run"]["id"])
    run = api.request(f"/repos/{repository}/actions/runs/{run_id}")
    validate_ci_identity(run, ci_workflow(api, repository), identifier)
    if run["id"] != run_id:
        raise EventError("CI wakeup run identity changed")
    try:
        return resolve_ci_pull(api, run, repository, identifier)
    except (OSError, ValueError, KeyError, TypeError) as error:
        document = live_repository(api, repository, identifier)
        invalidate_pulls(api, repository, identifier, document["default_branch"],
                         "CI wakeup unresolved; refresh exact-revision reuse analysis")
        raise EventError("Authenticated CI wakeup could not resolve its current exact pair; "
                         "invalidated open target PRs pending explicit refresh") from error


def resolve_ci_pull(api, run, repository, identifier):
    associations = run["pull_requests"]
    if not isinstance(associations, list) or len(associations) != 1:
        raise EventError("CI wakeup has no unambiguous pull request association")
    pull_number = number(associations[0]["number"])
    snapshot = pull_snapshot(api, repository, identifier, pull_number)
    exact_ci_association(run, snapshot)
    return pull_number


def workflow_pull_number(api, event, repository, identifier, signal_workflow, refresh_actor):
    # The event name is only a routing hint. Each branch authenticates the run
    # against GitHub's live API before it can change any status.
    run = api.request(f"/repos/{repository}/actions/runs/{number(event['workflow_run']['id'])}")
    if run["path"] == CI_WORKFLOW:
        return ci_pull_number(api, event, repository, identifier)
    return signal_pull_number(api, event, repository, identifier, signal_workflow, refresh_actor)


def resolve_signal_pull(api, repository, identifier, run):
    policy.immutable_sha(run["head_sha"])
    number(run["head_repository"]["id"])
    associations = run["pull_requests"]
    if not isinstance(associations, list):
        raise EventError("Malformed review signal associations")
    if not associations:
        associations = api.pages(f"/repos/{repository}/commits/{run['head_sha']}/pulls")
    candidates = set()
    for pull in associations:
        if pull["base"]["repo"]["id"] == identifier:
            candidates.add(number(pull["number"]))
    # The bounded artifact is a wakeup only, never detector or review evidence.
    # It resolves review runs whose fork/merge-ref associations are incomplete.
    pull_number = candidates.pop() if len(candidates) == 1 else signal_artifact(api, repository, run)
    current = pull_snapshot(api, repository, identifier, pull_number)
    if current["head_repository_id"] != run["head_repository"]["id"]:
        raise EventError("Review signal head repository does not match the live PR")
    return pull_number


def refresh_pull_number(api, event, repository, identifier, actor):
    policy.trusted_reviewers([actor])
    if event["action"] not in ("created", "edited") or event["sender"]["login"] != actor:
        raise EventError("Only the configured owner can request a refresh")
    document = live_repository(api, repository, identifier)
    if document["owner"]["login"] != actor:
        raise EventError("Refresh identity must be this repository's owner")
    pull_number = number(event["issue"]["number"])
    if not event["issue"].get("pull_request"):
        raise EventError("Refresh must be posted on a pull request")
    comment = api.request(f"/repos/{repository}/issues/comments/{number(event['comment']['id'])}")
    if (comment["user"]["login"] != actor or comment["body"].strip() != "/reuse-check"
            or comment["issue_url"] != f"https://api.github.com/repos/{repository}/issues/{pull_number}"):
        raise EventError("Live owner comment does not request this PR refresh")
    return pull_number


def prepare(api, event, event_name, repository, identifier, signal_workflow, refresh_actor):
    repository_name(repository)
    if event["repository"]["id"] != number(identifier):
        raise EventError("Event belongs to a different repository")
    if event_name == "pull_request_target":
        if event["action"] not in ("opened", "reopened", "synchronize", "edited", "ready_for_review"):
            raise EventError("Unsupported pull request target action")
        pull_number = number(event["number"])
    elif event_name == "workflow_run":
        pull_number = workflow_pull_number(api, event, repository, identifier, signal_workflow, refresh_actor)
    elif event_name == "issue_comment":
        pull_number = refresh_pull_number(api, event, repository, identifier, refresh_actor)
    else:
        raise EventError("Unsupported event; merge_group is not activated")
    snapshot = pull_snapshot(api, repository, identifier, pull_number)
    if event_name == "pull_request_target":
        pull = event["pull_request"]
        if (pull["number"] != pull_number or pull["base"]["sha"] != snapshot["base"]
                or pull["head"]["sha"] != snapshot["head"]
                or pull["base"]["repo"]["id"] != identifier
                or pull["head"]["repo"]["id"] != snapshot["head_repository_id"]):
            raise EventError("Event revisions do not match the live pull request")
    status(api, snapshot, "pending", "Protected reuse analysis and policy review pending")
    return snapshot


def invalidate_base(api, event, event_name, repository, identifier):
    document = live_repository(api, repository, identifier)
    if (event_name != "push" or event["repository"]["id"] != identifier
            or event["ref"] != "refs/heads/" + document["default_branch"] or event.get("deleted")):
        raise EventError("Base invalidation requires a push to the authenticated default branch")
    policy.immutable_sha(event["after"])
    branch = urllib.parse.quote(document["default_branch"], safe="")
    current = api.request(f"/repos/{repository}/git/ref/heads/{branch}")
    policy.immutable_sha(current["object"]["sha"])
    # If another push won the race, invalidate conservatively for that newer
    # base too. This event never grants a success or dispatches candidate code.
    return invalidate_pulls(api, repository, identifier, document["default_branch"])


def invalidate_pulls(api, repository, identifier, branch,
                     description="Protected base changed; refresh exact-revision reuse analysis", *,
                     dismissed_reviewer=None):
    count, failures = 0, 0
    for pull in api.pages(f"/repos/{repository}/pulls"):
        try:
            if pull["state"] != "open" or pull["base"]["ref"] != branch:
                continue
            # Invalidation must still erase old success while PR base metadata
            # catches up with a main push. This branch can only publish pending.
            snapshot = pull_snapshot(api, repository, identifier, number(pull["number"]),
                                     require_current_base=False)
            if dismissed_reviewer is not None and not owner_review_dismissed(
                    api, repository, snapshot["pull_number"], dismissed_reviewer):
                continue
            status(api, snapshot, "pending", description)
            count += 1
        except InactivePull:
            # A PR can close or retarget between list and detail requests.
            continue
        except (OSError, ValueError, KeyError, TypeError):
            # One API failure must not leave every later PR's status untouched.
            failures += 1
    if failures:
        raise EventError(f"Base invalidation failed for {failures} PRs after attempting every listed PR")
    return count


def status(api, snapshot, state, description):
    validate_snapshot(snapshot)
    payload = {"state": state, "context": CONTEXT, "description": description}
    run_id = os.environ.get("GITHUB_RUN_ID", "")
    if re.fullmatch(r"[1-9]\d*", run_id):
        payload["target_url"] = f"https://github.com/{snapshot['repository']}/actions/runs/{run_id}"
    api.request(f"/repos/{snapshot['repository']}/statuses/{snapshot['head']}", payload)


def git_environment(token=None):
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    for key in ("GH_TOKEN", "GITHUB_TOKEN", "GITHUB_OUTPUT", "GITHUB_ENV", "ACTIONS_RUNTIME_TOKEN"):
        environment.pop(key, None)
    environment.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_NO_REPLACE_OBJECTS="1", GIT_TERMINAL_PROMPT="0")
    if token:
        encoded = base64.b64encode(("x-access-token:" + token).encode()).decode()
        environment.update(GIT_CONFIG_COUNT="1", GIT_CONFIG_KEY_0="http.https://github.com/.extraheader",
                           GIT_CONFIG_VALUE_0="AUTHORIZATION: basic " + encoded)
    return environment


def git(root, *arguments, token=None, input_text=None):
    return subprocess.run(
        ["git", "-c", "core.hooksPath=/dev/null", "-c", "core.attributesFile=/dev/null",
         "-c", "merge.renormalize=false", *arguments], cwd=root, env=git_environment(token),
        input=input_text, capture_output=True, text=True, check=True).stdout.strip()


def candidate_commit(root, snapshot, token):
    if git(root, "rev-parse", "HEAD") != snapshot["base"]:
        raise EventError("Analysis must execute from the exact protected base checkout")
    configuration = git(root, "config", "--local", "--list")
    if re.search(r"^merge\..*\.driver=", configuration, re.MULTILINE):
        raise EventError("Custom local merge drivers are prohibited")
    git(root, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
        f"https://github.com/{snapshot['repository']}.git",
        f"+refs/pull/{snapshot['pull_number']}/head:refs/reuse/event-head", token=token)
    if git(root, "rev-parse", "refs/reuse/event-head") != snapshot["head"]:
        raise EventError("Fetched PR ref does not equal the selected immutable head")
    tree = git(root, "merge-tree", "--write-tree", snapshot["base"], snapshot["head"])
    policy.immutable_sha(tree)
    return git(root, "-c", "user.name=Reuse check", "-c", "user.email=reuse-check@users.noreply.github.com",
               "commit-tree", tree, "-p", snapshot["base"], "-p", snapshot["head"],
               input_text="Protected prospective reuse analysis\n")


def analyze(api, snapshot, root):
    validate_snapshot(snapshot)
    same_live_pair(api, snapshot)
    candidate = policy.immutable_sha(candidate_commit(root, snapshot, api.token))
    base = policy.immutable_sha(snapshot["base"])
    paths = subprocess.run(
        ["git", "diff", "--no-renames", "--name-only", "-z", base, candidate, "--"],
        cwd=root, env=git_environment(), capture_output=True, check=True).stdout
    policy_paths = list(policy.changed_policy_paths(policy.parse_changed_paths(paths)))
    command = [sys.executable, "-E", "-S", "-B", str(root / "scripts/check_reuse.py"),
               "--base", base, "--revision", candidate]
    completed = subprocess.run(command, cwd=root, env=git_environment(), capture_output=True, text=True)
    # Prefix captured candidate-derived diagnostics so they cannot become
    # GitHub workflow commands, and do not expose writable output-file paths.
    for line in (completed.stdout + completed.stderr).splitlines():
        print("reuse analysis | " + line)
    same_live_pair(api, snapshot)
    return {"version": 1, "snapshot": snapshot, "candidate": candidate,
            "detector_exit": completed.returncode, "policy_paths": policy_paths}


def validate_result(result, snapshot):
    if (not isinstance(result, dict) or set(result) != RESULT_KEYS
            or type(result["version"]) is not int or result["version"] != 1
            or result["snapshot"] != snapshot or type(result["detector_exit"]) is not int):
        raise EventError("Analysis outputs do not match the selected snapshot")
    policy.immutable_sha(result["candidate"])
    paths = result["policy_paths"]
    if (not isinstance(paths, list) or len(paths) != len(set(paths))
            or list(policy.changed_policy_paths(paths)) != paths):
        raise EventError("Malformed protected policy path evidence")
    return result


def review_evidence(api, snapshot, result, reviewers):
    same_live_pair(api, snapshot)
    reviews = api.pages(f"/repos/{snapshot['repository']}/pulls/{snapshot['pull_number']}/reviews")
    if result["policy_paths"]:
        policy.evaluate_signoff(
            reviews, repository=snapshot["repository"], pull_request=snapshot["pull_number"],
            base=snapshot["base"], head=snapshot["head"], allowed_reviewers=reviewers,
            reviews_complete=True)
    return reviews


def validate_suppression(receipt, snapshot):
    if not isinstance(receipt, dict) or set(receipt) != SUPPRESSION_KEYS:
        raise EventError("Missing or malformed suppression provenance receipt")
    if (receipt["headSHA"] != snapshot["head"] or receipt["baseSHA"] != snapshot["base"]
            or type(receipt["suppressionCount"]) is not int or receipt["suppressionCount"] != 0):
        raise EventError("Suppression provenance receipt does not establish this exact pair is clean")
    for key in ("runId", "runAttempt", "artifactId"):
        if number(receipt[key]) > 9007199254740991:
            raise EventError("Suppression provenance identifier exceeds the producer's integer range")
    return receipt


def ci_run_page(response, expected_total, head):
    total = response["total_count"]
    rows = response["workflow_runs"]
    if (type(total) is not int or not 0 <= total <= 1000
            or expected_total not in (None, total)
            or not isinstance(rows, list) or len(rows) > 100):
        raise EventError("CI run pagination is incomplete or exceeds the filtered API bound")
    for run in rows:
        number(run["id"])
        if run["head_sha"] != head or run["event"] != "pull_request":
            raise EventError("CI run listing does not match its exact head filter")
    return total, rows


def latest_ci_run(api, snapshot, *, allow_empty=False, workflow=None):
    endpoint = f"/repos/{snapshot['repository']}/actions/workflows/ci.yml/runs"
    runs, total = {}, None
    for page in range(1, 11):
        response = api.request(f"{endpoint}?per_page=100&page={page}&head_sha={snapshot['head']}&event=pull_request")
        total, rows = ci_run_page(response, total, snapshot["head"])
        for run in rows:
            if workflow is not None:
                validate_ci_identity(run, workflow, snapshot["repository_id"])
            if run["id"] in runs:
                raise EventError("CI run pagination repeated a producer")
            runs[run["id"]] = run
        if len(runs) == total:
            break
        if len(runs) > total or len(rows) < 100:
            raise EventError("CI run pagination ended before its exact total was established")
    if allow_empty and total == 0 and not runs:
        return None
    if not runs or len(runs) != total:
        raise EventError("CI run pagination did not establish the complete current producer set")
    # Select before checking success, base or association: a newer failed,
    # stale or malformed producer must never fall back to an older green run.
    selected = max(runs)
    run = api.request(f"/repos/{snapshot['repository']}/actions/runs/{selected}")
    if run["id"] != selected or number(run["run_attempt"]) < number(runs[selected]["run_attempt"]):
        raise EventError("Latest CI producer identity changed")
    listed = runs[selected]
    if (run["run_attempt"] == listed["run_attempt"]
            and listed["status"] == "completed" and listed["conclusion"] == "success"
            and run["status"] in CI_NONTERMINAL and run["conclusion"] is None):
        raise EventError("Successful CI producer regressed during selection")
    return run


def current_ci(api, snapshot, expected=None, *, allow_registration=False, completed_proof=False):
    """Separate scheduling from evidence errors without older-green fallback."""
    same_live_pair(api, snapshot)
    workflow = ci_workflow(api, snapshot["repository"])
    run = latest_ci_run(api, snapshot, allow_empty=allow_registration, workflow=workflow)
    if run is None:
        if expected is not None:
            raise EventError("Previously observed CI producer disappeared")
        raise CIDeferred("Waiting for CI registration for the current pull request")
    validate_ci_identity(run, workflow, snapshot["repository_id"])
    exact_ci_association(run, snapshot)
    epoch = (run["id"], run["run_attempt"])
    if expected is not None and epoch < expected:
        raise EventError("Current CI producer regressed behind the bound proof")
    if run["status"] in CI_NONTERMINAL and run["conclusion"] is None:
        if completed_proof and epoch == expected:
            raise EventError("Successful CI proof regressed within the same run attempt")
        raise CIDeferred("Waiting for the authenticated current CI attempt")
    if run["status"] != "completed" or run["conclusion"] != "success":
        raise EventError("Latest CI producer did not complete successfully")
    if expected is not None and epoch != expected:
        raise CIDeferred("A newer authenticated CI attempt requires fresh proof")
    return run


def prepare_readiness(api, snapshot):
    try:
        current_ci(api, snapshot, allow_registration=True)
    except CIDeferred as error:
        print(str(error))
        return "waiting"
    return "ready"


def validate_deferred(document, snapshot):
    if (not isinstance(document, dict) or set(document) != DEFERRED_KEYS
            or type(document["version"]) is not int or document["version"] != 1
            or validate_snapshot(document["snapshot"]) != snapshot
            or document["reason"] not in ("registration", "pending", "superseded")):
        raise EventError("Malformed suppression deferral")
    if document["reason"] == "registration":
        if document["runId"] is not None or document["runAttempt"] is not None:
            raise EventError("Registration deferral cannot claim a producer")
        return None
    epoch = (number(document["runId"]), number(document["runAttempt"]))
    if any(value > 9007199254740991 for value in epoch):
        raise EventError("Suppression deferral producer exceeds the safe identifier range")
    return epoch


def revalidate_deferred(api, snapshot, document):
    expected = validate_deferred(document, snapshot)
    try:
        current_ci(api, snapshot, expected, allow_registration=expected is None)
    except CIDeferred:
        pass
    same_live_pair(api, snapshot)
    # The protected read-only job supplied no proof. If CI completed meanwhile,
    # its completion wakeup must verify it. Do not overwrite a newer publisher's
    # valid success with an older waiting-only write.


def producer_time(value):
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value):
        raise EventError("Malformed CI artifact timestamp")
    return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")


def validate_ci_artifact(artifact, run, snapshot, receipt):
    if (artifact["id"] != receipt["artifactId"]
            or artifact["name"] != f"pr-report-inputs-{snapshot['pull_number']}"
            or artifact["expired"] is not False
            or type(artifact["size_in_bytes"]) is not int or not 0 < artifact["size_in_bytes"] <= 8388608):
        raise EventError("Suppression artifact identity, expiry or size changed")
    producer = artifact["workflow_run"]
    if (producer["id"] != run["id"] or producer["head_sha"] != snapshot["head"]
            or producer["repository_id"] != snapshot["repository_id"]
            or producer["head_repository_id"] != snapshot["head_repository_id"]):
        raise EventError("Suppression artifact belongs to a different producer")
    if not producer_time(run["run_started_at"]) <= producer_time(artifact["created_at"]) <= producer_time(run["updated_at"]):
        raise EventError("Suppression artifact is outside the selected producer attempt")
    digest = artifact["digest"]
    if not isinstance(digest, str) or not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
        raise EventError("Suppression artifact has no immutable digest")


def suppression_evidence(api, snapshot, receipt):
    validate_suppression(receipt, snapshot)
    repository = snapshot["repository"]
    run = current_ci(api, snapshot, (receipt["runId"], receipt["runAttempt"]), completed_proof=True)
    artifact = api.request(f"/repos/{repository}/actions/artifacts/{receipt['artifactId']}")
    validate_ci_artifact(artifact, run, snapshot, receipt)
    return artifact


def publish(api, snapshot, result, analysis_result, reviewers, suppression=None, suppression_result="failure", deferred=None):
    validate_snapshot(snapshot)
    try:
        if analysis_result != "success":
            raise EventError("Read-only analysis job did not succeed")
        validate_result(result, snapshot)
        if result["detector_exit"] != 0:
            raise EventError("Protected reuse detectors did not pass")
        if suppression_result != "success":
            raise EventError("Read-only suppression provenance job did not succeed")
        if deferred:
            validate_deferred(deferred, snapshot)
            if suppression not in (None, {}):
                raise EventError("Suppression deferral cannot also supply an approval receipt")
        else:
            if deferred not in (None, {}):
                raise EventError("Malformed suppression deferral")
            validate_suppression(suppression, snapshot)
        first = review_evidence(api, snapshot, result, reviewers)
        second = review_evidence(api, snapshot, result, reviewers)
        if first != second:
            raise EventError("Live review evidence changed during publication")
        if deferred:
            revalidate_deferred(api, snapshot, deferred)
            return
        producer = suppression_evidence(api, snapshot, suppression)
        same_live_pair(api, snapshot)
        status(api, snapshot, "success", "Protected reuse, suppression provenance and policy review passed")
        # GitHub's status write is not conditional on PR/review state. Correct
        # a write overtaken by a withdrawal or base update as soon as observed.
        after_publication = review_evidence(api, snapshot, result, reviewers)
        if after_publication != second:
            raise EventError("Live review evidence changed during the status write")
        if suppression_evidence(api, snapshot, suppression) != producer:
            raise EventError("Suppression producer metadata changed during the status write")
        same_live_pair(api, snapshot)
    except CIDeferred:
        # A genuine new CI epoch invalidates this proof without making an
        # optional PR-head job permanently fail. Correct our own old success.
        status(api, snapshot, "pending", "Current CI requires fresh protected reuse evidence")
    except (OSError, ValueError, KeyError, TypeError, http.client.HTTPException) as error:
        status(api, snapshot, "failure", "Reuse, suppression provenance or policy review failed; see run")
        raise EventError(str(error)) from error


def emit(path, name, document):
    encoded = json.dumps(document, sort_keys=True, separators=(",", ":"))
    evidence_path(path, name).write_text(encoded + "\n")
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as stream:
            stream.write(name + "=" + encoded + "\n")
            if name == "snapshot":
                for key in ("base", "head", "pull_number"):
                    stream.write(f"{key}={document[key]}\n")


def emit_readiness(readiness):
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as stream:
            stream.write(f"readiness={readiness}\n")


def publication_document(path, name, outcome):
    if outcome != "success":
        return None
    try:
        return json.loads(evidence_path(path, name).read_text(), object_pairs_hook=policy.unique_object)
    except (OSError, ValueError, KeyError, TypeError):
        # Keep malformed/missing successful-job output inside publish's failure
        # path so it actively corrects an earlier success rather than merely exiting.
        return None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("invalidate-base")
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("--snapshot", required=True)
    prepare_parser.add_argument("--signal-workflow", default=".github/workflows/reuse-review-signal.yml")
    prepare_parser.add_argument("--refresh-actor", required=True)
    analyze_parser = commands.add_parser("analyze")
    analyze_parser.add_argument("--snapshot", required=True)
    analyze_parser.add_argument("--result", required=True)
    publish_parser = commands.add_parser("publish")
    publish_parser.add_argument("--snapshot", required=True)
    publish_parser.add_argument("--result", required=True)
    publish_parser.add_argument("--analysis-result", choices=("success", "failure", "cancelled", "skipped"), required=True)
    publish_parser.add_argument("--suppression", required=True)
    publish_parser.add_argument("--deferred")
    publish_parser.add_argument("--suppression-result", choices=("success", "failure", "cancelled", "skipped"), required=True)
    publish_parser.add_argument("--reviewer", action="append", required=True)
    args = parser.parse_args(argv)
    try:
        api = GitHub(os.environ.get("GH_TOKEN"))
        if args.command in ("prepare", "invalidate-base"):
            event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
            event_arguments = (api, event, os.environ["GITHUB_EVENT_NAME"],
                               os.environ["GITHUB_REPOSITORY"], int(os.environ["GITHUB_REPOSITORY_ID"]))
            if args.command == "invalidate-base":
                print(f"Invalidated {invalidate_base(*event_arguments)} open PR statuses")
            else:
                snapshot = prepare(*event_arguments, args.signal_workflow, args.refresh_actor)
                emit(args.snapshot, "snapshot", snapshot)
                emit_readiness(prepare_readiness(api, snapshot))
        else:
            snapshot = validate_snapshot(json.loads(evidence_path(args.snapshot, "snapshot").read_text()))
            if args.command == "analyze":
                result = analyze(api, snapshot, Path(__file__).resolve().parents[1])
                emit(args.result, "result", result)
                return 0 if result["detector_exit"] == 0 else 1
            result = publication_document(args.result, "result", args.analysis_result)
            suppression = publication_document(args.suppression, "suppression", args.suppression_result)
            deferred = None
            if args.deferred:
                deferred = publication_document(args.deferred, "deferred", args.suppression_result)
                if deferred is None:
                    deferred = {"invalid": True}
            publish(api, snapshot, result, args.analysis_result, args.reviewer,
                    suppression, args.suppression_result, deferred)
        return 0
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print("Protected reuse controller failed: " + str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
