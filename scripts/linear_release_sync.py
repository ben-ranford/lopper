#!/usr/bin/env python3
"""Reconcile configured GitHub milestone issues into Linear release mirrors."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


LINEAR_GRAPHQL_URL = "https://api.linear.app/graphql"
GITHUB_API_URL = "https://api.github.com"
DEFAULT_CONFIG_PATH = Path(".github/linear-release-sync.json")
SYNC_MARKER_PREFIX = "github-linear-release-sync"
SYNC_KEY_PATTERN = re.compile(
    r"github:(?P<repository>[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#(?P<number>\d+)"
)
LINEAR_TITLE_LIMIT = 255
LINEAR_LABELS_MALFORMED = "Linear issue labels response is malformed"


class SyncError(RuntimeError):
    """Raised when reconciliation cannot continue safely."""


class JsonHttpClient:
    """Small JSON HTTP client with consistent error reporting."""

    def request(
        self,
        method: str,
        url: str,
        *,
        headers: dict[str, str] | None = None,
        payload: dict[str, Any] | None = None,
    ) -> Any:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        request_headers = {"Accept": "application/json", **(headers or {})}
        if body is not None:
            request_headers["Content-Type"] = "application/json"
        request = Request(url, data=body, headers=request_headers, method=method)
        try:
            with urlopen(request, timeout=30) as response:
                response_body = response.read()
        except HTTPError as error:
            detail = error.read().decode("utf-8", errors="replace").strip()
            raise SyncError(
                f"{method} {url} returned HTTP {error.code}: {detail or error.reason}"
            ) from error
        except URLError as error:
            raise SyncError(f"{method} {url} failed: {error.reason}") from error

        if not response_body:
            return None
        try:
            return json.loads(response_body)
        except json.JSONDecodeError as error:
            raise SyncError(f"{method} {url} returned invalid JSON") from error


@dataclass(frozen=True)
class MilestoneMapping:
    release_label_id: str
    project_milestone_id: str


@dataclass(frozen=True)
class SyncConfig:
    repository: str
    team_id: str
    project_id: str
    open_state_id: str
    closed_state_id: str
    mirror_label_id: str
    default_type_label_id: str
    type_label_rules: tuple[tuple[frozenset[str], str], ...]
    milestones: dict[str, MilestoneMapping]

    @property
    def managed_label_ids(self) -> frozenset[str]:
        configured = {
            self.mirror_label_id,
            self.default_type_label_id,
            *(label_id for _, label_id in self.type_label_rules),
            *(mapping.release_label_id for mapping in self.milestones.values()),
        }
        return frozenset(configured)

    def type_label_for(self, github_labels: Iterable[str]) -> str:
        names = {name.casefold() for name in github_labels}
        for matching_names, label_id in self.type_label_rules:
            if names.intersection(matching_names):
                return label_id
        return self.default_type_label_id


def _required_string(container: dict[str, Any], key: str, context: str) -> str:
    value = container.get(key)
    if not isinstance(value, str) or not value.strip():
        raise SyncError(f"{context}.{key} must be a non-empty string")
    return value


def _required_object(container: dict[str, Any], key: str, context: str) -> dict[str, Any]:
    value = container.get(key)
    if not isinstance(value, dict):
        raise SyncError(f"{context}.{key} must be an object")
    return value


def _read_config(path: Path) -> dict[str, Any]:
    workspace = Path.cwd().resolve()
    try:
        resolved = path.resolve(strict=True)
    except OSError as error:
        raise SyncError(f"cannot resolve config {path}: {error}") from error
    try:
        resolved.relative_to(workspace)
    except ValueError as error:
        raise SyncError(f"config path must stay within workspace {workspace}") from error
    if not resolved.is_file():
        raise SyncError(f"config {resolved} must be a regular file")

    try:
        content = resolved.read_text(encoding="utf-8")
    except OSError as error:
        raise SyncError(f"cannot read config {resolved}: {error}") from error
    try:
        raw = json.loads(content)
    except json.JSONDecodeError as error:
        raise SyncError(f"config {resolved} is not valid JSON: {error}") from error
    if not isinstance(raw, dict):
        raise SyncError("config root must be an object")
    if raw.get("version") != 1:
        raise SyncError("config.version must be 1")
    return raw


def _parse_type_rules(linear: dict[str, Any]) -> tuple[tuple[frozenset[str], str], ...]:
    rules = linear.get("type_label_rules")
    if not isinstance(rules, list):
        raise SyncError("config.linear.type_label_rules must be an array")

    parsed: list[tuple[frozenset[str], str]] = []
    for index, rule in enumerate(rules):
        context = f"linear.type_label_rules[{index}]"
        if not isinstance(rule, dict):
            raise SyncError(f"{context} must be an object")
        github_labels = rule.get("github_labels")
        if not isinstance(github_labels, list):
            raise SyncError(f"{context}.github_labels must be an array")
        names = frozenset(
            str(name).casefold() for name in github_labels if str(name).strip()
        )
        if not names:
            raise SyncError(f"{context}.github_labels must not be empty")
        parsed.append((names, _required_string(rule, "linear_label_id", context)))
    return tuple(parsed)


def _parse_milestones(raw: dict[str, Any]) -> dict[str, MilestoneMapping]:
    milestones = raw.get("milestones")
    if not isinstance(milestones, dict) or not milestones:
        raise SyncError("config.milestones must contain at least one mapping")

    mappings: dict[str, MilestoneMapping] = {}
    for title, mapping in milestones.items():
        context = f"milestones[{title!r}]"
        if not isinstance(title, str) or not title.strip() or not isinstance(mapping, dict):
            raise SyncError("each milestone mapping must have a non-empty title and object value")
        mappings[title] = MilestoneMapping(
            release_label_id=_required_string(mapping, "release_label_id", context),
            project_milestone_id=_required_string(mapping, "project_milestone_id", context),
        )
    return mappings


def load_config(path: Path) -> SyncConfig:
    raw = _read_config(path)
    github = _required_object(raw, "github", "config")
    linear = _required_object(raw, "linear", "config")
    states = _required_object(linear, "states", "linear")
    labels = _required_object(linear, "labels", "linear")

    return SyncConfig(
        repository=_required_string(github, "repository", "github"),
        team_id=_required_string(linear, "team_id", "linear"),
        project_id=_required_string(linear, "project_id", "linear"),
        open_state_id=_required_string(states, "open", "linear.states"),
        closed_state_id=_required_string(states, "closed", "linear.states"),
        mirror_label_id=_required_string(labels, "github_mirror", "linear.labels"),
        default_type_label_id=_required_string(
            labels, "default_type", "linear.labels"
        ),
        type_label_rules=_parse_type_rules(linear),
        milestones=_parse_milestones(raw),
    )


@dataclass(frozen=True)
class GitHubIssue:
    number: int
    title: str
    body: str
    url: str
    state: str
    labels: tuple[str, ...]
    milestone: str | None

    @property
    def sync_key(self) -> str:
        repository = self.url.split("/issues/", 1)[0].removeprefix("https://github.com/")
        return f"github:{repository}#{self.number}"


def parse_github_issue(raw: dict[str, Any]) -> GitHubIssue:
    try:
        number = raw["number"]
        title = raw["title"]
        url = raw["html_url"]
        state = raw["state"]
    except KeyError as error:
        raise SyncError(f"GitHub issue is missing {error.args[0]}") from error
    if not isinstance(number, int) or not all(isinstance(value, str) for value in (title, url, state)):
        raise SyncError("GitHub issue has invalid number, title, URL, or state")

    milestone_raw = raw.get("milestone")
    milestone = milestone_raw.get("title") if isinstance(milestone_raw, dict) else None
    labels: list[str] = []
    for label in raw.get("labels") or []:
        if isinstance(label, dict) and isinstance(label.get("name"), str):
            labels.append(label["name"])

    return GitHubIssue(
        number=number,
        title=title,
        body=raw.get("body") if isinstance(raw.get("body"), str) else "",
        url=url,
        state=state,
        labels=tuple(labels),
        milestone=milestone if isinstance(milestone, str) else None,
    )


class GitHubClient:
    def __init__(
        self,
        token: str,
        repository: str,
        http: JsonHttpClient,
        api_url: str = GITHUB_API_URL,
    ) -> None:
        self._repository = repository
        self._http = http
        self._api_url = api_url.rstrip("/")
        self._headers = {
            "Authorization": f"Bearer {token}",
            "X-GitHub-Api-Version": "2022-11-28",
        }

    def _paginate(self, path: str, parameters: dict[str, Any]) -> list[dict[str, Any]]:
        results: list[dict[str, Any]] = []
        page = 1
        while True:
            query = urlencode({**parameters, "per_page": 100, "page": page})
            response = self._http.request(
                "GET",
                f"{self._api_url}/repos/{self._repository}/{path}?{query}",
                headers=self._headers,
            )
            if not isinstance(response, list):
                raise SyncError(f"GitHub {path} response must be an array")
            results.extend(item for item in response if isinstance(item, dict))
            if len(response) < 100:
                return results
            page += 1

    def configured_milestone_numbers(self, titles: Iterable[str]) -> dict[str, int]:
        expected = set(titles)
        found: dict[str, int] = {}
        for milestone in self._paginate("milestones", {"state": "all"}):
            title = milestone.get("title")
            number = milestone.get("number")
            if title in expected and isinstance(number, int):
                found[title] = number
        missing = sorted(expected.difference(found))
        if missing:
            raise SyncError(f"configured GitHub milestones do not exist: {', '.join(missing)}")
        return found

    def issues_for_milestone(self, milestone_number: int) -> list[GitHubIssue]:
        raw_issues = self._paginate(
            "issues", {"state": "all", "milestone": milestone_number}
        )
        return [
            parse_github_issue(issue)
            for issue in raw_issues
            if "pull_request" not in issue
        ]

    def issue(self, number: int) -> GitHubIssue:
        response = self._http.request(
            "GET",
            f"{self._api_url}/repos/{self._repository}/issues/{number}",
            headers=self._headers,
        )
        if not isinstance(response, dict) or "pull_request" in response:
            raise SyncError(f"GitHub issue #{number} response is malformed")
        return parse_github_issue(response)


@dataclass(frozen=True)
class LinearIssue:
    id: str
    identifier: str
    title: str
    description: str
    state_id: str
    project_id: str | None
    project_milestone_id: str | None
    label_ids: frozenset[str]


class LinearClient:
    def __init__(
        self,
        api_key: str,
        http: JsonHttpClient,
        endpoint: str = LINEAR_GRAPHQL_URL,
    ) -> None:
        self._http = http
        self._endpoint = endpoint
        self._headers = {"Authorization": api_key}

    def _graphql(self, query: str, variables: dict[str, Any]) -> dict[str, Any]:
        response = self._http.request(
            "POST",
            self._endpoint,
            headers=self._headers,
            payload={"query": query, "variables": variables},
        )
        if not isinstance(response, dict):
            raise SyncError("Linear GraphQL response must be an object")
        errors = response.get("errors")
        if errors:
            messages = "; ".join(
                str(error.get("message", error)) if isinstance(error, dict) else str(error)
                for error in errors
            )
            raise SyncError(f"Linear GraphQL error: {messages}")
        data = response.get("data")
        if not isinstance(data, dict):
            raise SyncError("Linear GraphQL response is missing data")
        return data

    def issues_attached_to(self, url: str) -> list[LinearIssue]:
        data = self._graphql(
            """
            query IssuesAttachedToURL($url: String!) {
              attachmentsForURL(url: $url) {
                nodes {
                  issue {
                    id
                    identifier
                    title
                    description
                    state { id }
                    project { id }
                    projectMilestone { id }
                    labels(first: 50) {
                      nodes { id }
                      pageInfo { hasNextPage endCursor }
                    }
                  }
                }
              }
            }
            """,
            {"url": url},
        )
        connection = data.get("attachmentsForURL")
        nodes = connection.get("nodes") if isinstance(connection, dict) else None
        if not isinstance(nodes, list):
            raise SyncError("Linear attachmentsForURL response is malformed")

        issues: dict[str, LinearIssue] = {}
        for node in nodes:
            issue = node.get("issue") if isinstance(node, dict) else None
            if not isinstance(issue, dict):
                continue
            parsed = self._parse_issue(issue)
            issues[parsed.id] = parsed
        return list(issues.values())

    def project_issues(self, project_id: str) -> list[LinearIssue]:
        issues: list[LinearIssue] = []
        after: str | None = None
        while True:
            data = self._graphql(
                """
                query ProjectIssues($id: String!, $after: String) {
                  project(id: $id) {
                    issues(first: 50, after: $after) {
                      nodes {
                        id
                        identifier
                        title
                        description
                        state { id }
                        project { id }
                        projectMilestone { id }
                        labels(first: 50) {
                          nodes { id }
                          pageInfo { hasNextPage endCursor }
                        }
                      }
                      pageInfo { hasNextPage endCursor }
                    }
                  }
                }
                """,
                {"id": project_id, "after": after},
            )
            project = data.get("project")
            connection = project.get("issues") if isinstance(project, dict) else None
            nodes = connection.get("nodes") if isinstance(connection, dict) else None
            page_info = connection.get("pageInfo") if isinstance(connection, dict) else None
            if not isinstance(nodes, list) or not isinstance(page_info, dict):
                raise SyncError("Linear project issues response is malformed")
            issues.extend(
                self._parse_issue(node) for node in nodes if isinstance(node, dict)
            )
            if page_info.get("hasNextPage") is not True:
                return issues
            after = page_info.get("endCursor")
            if not isinstance(after, str) or not after:
                raise SyncError("Linear project issues pagination is missing endCursor")

    def _parse_issue(self, raw: dict[str, Any]) -> LinearIssue:
        try:
            issue_id = raw["id"]
            identifier = raw["identifier"]
            title = raw["title"]
            state_id = raw["state"]["id"]
        except (KeyError, TypeError) as error:
            raise SyncError("Linear attachment returned an incomplete issue") from error
        project = raw.get("project")
        milestone = raw.get("projectMilestone")
        labels = raw.get("labels")
        return LinearIssue(
            id=str(issue_id),
            identifier=str(identifier),
            title=str(title),
            description=raw.get("description") if isinstance(raw.get("description"), str) else "",
            state_id=str(state_id),
            project_id=str(project["id"]) if isinstance(project, dict) and project.get("id") else None,
            project_milestone_id=(
                str(milestone["id"])
                if isinstance(milestone, dict) and milestone.get("id")
                else None
            ),
            label_ids=self._all_label_ids(str(issue_id), labels),
        )

    def _all_label_ids(self, issue_id: str, connection: Any) -> frozenset[str]:
        label_ids: set[str] = set()
        current = connection
        while True:
            if not isinstance(current, dict):
                raise SyncError(LINEAR_LABELS_MALFORMED)
            nodes = current.get("nodes")
            if not isinstance(nodes, list):
                raise SyncError(LINEAR_LABELS_MALFORMED)
            label_ids.update(
                str(label["id"])
                for label in nodes
                if isinstance(label, dict) and label.get("id")
            )

            page_info = current.get("pageInfo")
            if page_info is None:
                return frozenset(label_ids)
            if not isinstance(page_info, dict):
                raise SyncError("Linear issue labels pagination is malformed")
            if page_info.get("hasNextPage") is not True:
                return frozenset(label_ids)
            after = page_info.get("endCursor")
            if not isinstance(after, str) or not after:
                raise SyncError("Linear issue labels pagination is missing endCursor")
            current = self._label_page(issue_id, after)

    def _label_page(self, issue_id: str, after: str) -> Any:
        data = self._graphql(
            """
            query IssueLabels($id: String!, $after: String!) {
              issue(id: $id) {
                labels(first: 50, after: $after) {
                  nodes { id }
                  pageInfo { hasNextPage endCursor }
                }
              }
            }
            """,
            {"id": issue_id, "after": after},
        )
        issue = data.get("issue")
        if not isinstance(issue, dict):
            raise SyncError(LINEAR_LABELS_MALFORMED)
        return issue.get("labels")

    def create_issue(self, values: dict[str, Any]) -> tuple[str, str]:
        data = self._graphql(
            """
            mutation CreateMirror($input: IssueCreateInput!) {
              issueCreate(input: $input) {
                success
                issue { id identifier }
              }
            }
            """,
            {"input": values},
        )
        payload = data.get("issueCreate")
        issue = payload.get("issue") if isinstance(payload, dict) else None
        if not isinstance(payload, dict) or payload.get("success") is not True or not isinstance(issue, dict):
            raise SyncError("Linear issueCreate did not return a successful issue")
        return str(issue["id"]), str(issue["identifier"])

    def issue_identity(self, issue_id: str) -> tuple[str, str, str]:
        data = self._graphql(
            """
            query MirrorIdentity($id: String!) {
              issue(id: $id) { id identifier description }
            }
            """,
            {"id": issue_id},
        )
        issue = data.get("issue")
        if not isinstance(issue, dict):
            raise SyncError(f"Linear issue {issue_id} was not found")
        identifier = issue.get("identifier")
        description = issue.get("description")
        if not isinstance(identifier, str):
            raise SyncError(f"Linear issue {issue_id} identity is malformed")
        return (
            str(issue.get("id", issue_id)),
            identifier,
            description if isinstance(description, str) else "",
        )

    def update_issue(self, issue_id: str, values: dict[str, Any]) -> None:
        data = self._graphql(
            """
            mutation UpdateMirror($id: String!, $input: IssueUpdateInput!) {
              issueUpdate(id: $id, input: $input) { success }
            }
            """,
            {"id": issue_id, "input": values},
        )
        payload = data.get("issueUpdate")
        if not isinstance(payload, dict) or payload.get("success") is not True:
            raise SyncError("Linear issueUpdate was not successful")

    def attach_url(self, issue_id: str, issue: GitHubIssue) -> None:
        data = self._graphql(
            """
            mutation AttachGitHubIssue($input: AttachmentCreateInput!) {
              attachmentCreate(input: $input) { success }
            }
            """,
            {
                "input": {
                    "issueId": issue_id,
                    "title": f"GitHub issue #{issue.number}",
                    "subtitle": _truncate(issue.title, LINEAR_TITLE_LIMIT),
                    "url": issue.url,
                    "metadata": {
                        "source": SYNC_MARKER_PREFIX,
                        "githubIssueNumber": issue.number,
                    },
                }
            },
        )
        payload = data.get("attachmentCreate")
        if not isinstance(payload, dict) or payload.get("success") is not True:
            raise SyncError("Linear attachmentCreate was not successful")


def _sync_marker(sync_key: str) -> str:
    return f"<!-- {SYNC_MARKER_PREFIX}:{sync_key} -->"


def deterministic_issue_id(sync_key: str) -> str:
    """Return a stable UUIDv4-shaped ID for one GitHub mirror."""
    digest = hashlib.sha256(
        f"{SYNC_MARKER_PREFIX}:{sync_key}".encode("utf-8")
    ).digest()
    return str(uuid.UUID(bytes=digest[:16], version=4))


def is_automation_owned(description: str, sync_key: str) -> bool:
    legacy_marker = f"**Sync key:** `{sync_key}`"
    return _sync_marker(sync_key) in description or legacy_marker in description


def description_sync_key(description: str) -> str | None:
    if SYNC_MARKER_PREFIX not in description and "**Sync key:**" not in description:
        return None
    match = SYNC_KEY_PATTERN.search(description)
    return match.group(0) if match else None


def is_recoverable_mirror(
    issue: LinearIssue, sync_key: str, mirror_label_id: str
) -> bool:
    return (
        _sync_marker(sync_key) in issue.description
        or mirror_label_id in issue.label_ids
    )


def render_description(issue: GitHubIssue) -> str:
    release = issue.milestone or "Unassigned"
    body = issue.body.strip() or "_No GitHub issue description provided._"
    return "\n".join(
        (
            _sync_marker(issue.sync_key),
            f"**GitHub mirror:** [#{issue.number}]({issue.url})",
            f"**Sync key:** `{issue.sync_key}`",
            f"**Release target:** `{release}`",
            f"**GitHub state:** `{issue.state}`",
            "",
            "## GitHub issue",
            "",
            body,
            "",
            "---",
            "This mirror is managed from GitHub. Edit the linked GitHub issue instead.",
        )
    )


def _truncate(value: str, limit: int) -> str:
    if len(value) <= limit:
        return value
    return f"{value[: limit - 1].rstrip()}…"


def render_title(issue: GitHubIssue) -> str:
    return _truncate(f"GH #{issue.number} — {issue.title}", LINEAR_TITLE_LIMIT)


@dataclass(frozen=True)
class DesiredIssue:
    title: str
    description: str
    state_id: str
    project_id: str
    project_milestone_id: str | None
    managed_label_ids: frozenset[str]


class SyncEngine:
    def __init__(
        self,
        config: SyncConfig,
        linear: LinearClient,
        dry_run: bool = False,
        project_issues: Iterable[LinearIssue] | None = None,
        recover_unmapped_orphans: bool = True,
    ) -> None:
        self._config = config
        self._linear = linear
        self._dry_run = dry_run
        self._mirrors_by_key = (
            self._index_mirrors(project_issues) if project_issues is not None else None
        )
        self._recover_unmapped_orphans = recover_unmapped_orphans

    def _index_mirrors(
        self, issues: Iterable[LinearIssue]
    ) -> dict[str, list[LinearIssue]]:
        mirrors: dict[str, list[LinearIssue]] = {}
        for issue in issues:
            key = description_sync_key(issue.description)
            if key is not None and is_recoverable_mirror(
                issue, key, self._config.mirror_label_id
            ):
                mirrors.setdefault(key, []).append(issue)
        return mirrors

    def _desired(self, issue: GitHubIssue) -> DesiredIssue:
        mapping = self._config.milestones.get(issue.milestone or "")
        labels = {self._config.mirror_label_id, self._config.type_label_for(issue.labels)}
        if mapping is not None:
            labels.add(mapping.release_label_id)
        return DesiredIssue(
            title=render_title(issue),
            description=render_description(issue),
            state_id=(
                self._config.closed_state_id
                if issue.state == "closed"
                else self._config.open_state_id
            ),
            project_id=self._config.project_id,
            project_milestone_id=(mapping.project_milestone_id if mapping else None),
            managed_label_ids=frozenset(labels),
        )

    def sync(self, issue: GitHubIssue) -> str:
        attached = self._linear.issues_attached_to(issue.url)
        if len(attached) > 1:
            identifiers = ", ".join(sorted(item.identifier for item in attached))
            raise SyncError(
                f"{issue.url} is attached to multiple Linear issues ({identifiers}); resolve the duplicate before syncing"
            )
        mapping = self._config.milestones.get(issue.milestone or "")
        if not attached:
            if mapping is None and not self._recover_unmapped_orphans:
                return f"skip GH #{issue.number}: milestone is not mapped"
            orphaned = self._orphaned_mirrors(issue.sync_key)
            if len(orphaned) > 1:
                identifiers = ", ".join(sorted(item.identifier for item in orphaned))
                raise SyncError(
                    f"{issue.sync_key} appears in multiple Linear mirrors ({identifiers}); resolve the duplicate before syncing"
                )
            if orphaned:
                return self._recover(issue, orphaned[0])
            if mapping is None:
                return f"skip GH #{issue.number}: milestone is not mapped"
            return self._create(issue)

        current = attached[0]
        if not is_automation_owned(current.description, issue.sync_key):
            return f"covered GH #{issue.number}: {current.identifier} is hand-authored"
        return self._update(issue, current)

    def _orphaned_mirrors(self, sync_key: str) -> list[LinearIssue]:
        if self._mirrors_by_key is None:
            self._mirrors_by_key = self._index_mirrors(
                self._linear.project_issues(self._config.project_id)
            )
        return self._mirrors_by_key.get(sync_key, [])

    def _recover(self, issue: GitHubIssue, current: LinearIssue) -> str:
        if self._dry_run:
            return f"dry-run recover GH #{issue.number}: {current.identifier}"
        self._linear.attach_url(current.id, issue)
        result = self._update(issue, current)
        return f"recovered GH #{issue.number}: {current.identifier}; {result}"

    def _create(self, issue: GitHubIssue) -> str:
        desired = self._desired(issue)
        expected_id = deterministic_issue_id(issue.sync_key)
        values = {
            "id": expected_id,
            "teamId": self._config.team_id,
            "projectId": desired.project_id,
            "projectMilestoneId": desired.project_milestone_id,
            "title": desired.title,
            "description": desired.description,
            "stateId": desired.state_id,
            "labelIds": sorted(desired.managed_label_ids),
        }
        if self._dry_run:
            return f"dry-run create GH #{issue.number}"
        joined_concurrent_create = False
        try:
            issue_id, identifier = self._linear.create_issue(values)
        except SyncError as create_error:
            try:
                issue_id, identifier, description = self._linear.issue_identity(
                    expected_id
                )
            except SyncError:
                raise create_error
            if issue_id != expected_id or not is_automation_owned(
                description, issue.sync_key
            ):
                raise create_error
            joined_concurrent_create = True
        self._linear.attach_url(issue_id, issue)
        if joined_concurrent_create:
            return f"joined concurrent create GH #{issue.number}: {identifier}"
        return f"created GH #{issue.number}: {identifier}"

    def _update(self, issue: GitHubIssue, current: LinearIssue) -> str:
        desired = self._desired(issue)
        label_ids = (
            current.label_ids.difference(self._config.managed_label_ids)
            | desired.managed_label_ids
        )
        changes: dict[str, Any] = {}
        comparisons = (
            ("title", current.title, desired.title),
            ("description", current.description, desired.description),
            ("stateId", current.state_id, desired.state_id),
            ("projectId", current.project_id, desired.project_id),
            (
                "projectMilestoneId",
                current.project_milestone_id,
                desired.project_milestone_id,
            ),
            ("labelIds", current.label_ids, frozenset(label_ids)),
        )
        for field, existing, wanted in comparisons:
            if existing != wanted:
                changes[field] = sorted(wanted) if field == "labelIds" else wanted

        if not changes:
            return f"unchanged GH #{issue.number}: {current.identifier}"
        if self._dry_run:
            return f"dry-run update GH #{issue.number}: {current.identifier} ({', '.join(changes)})"
        self._linear.update_issue(current.id, changes)
        return f"updated GH #{issue.number}: {current.identifier} ({', '.join(changes)})"


def _read_event_issue_number(path: Path, repository: str) -> int:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise SyncError(f"cannot read GitHub event payload {path}: {error}") from error
    event_repository = payload.get("repository") if isinstance(payload, dict) else None
    full_name = event_repository.get("full_name") if isinstance(event_repository, dict) else None
    if full_name != repository:
        raise SyncError(f"event repository {full_name!r} does not match configured {repository!r}")
    issue = payload.get("issue") if isinstance(payload, dict) else None
    if not isinstance(issue, dict):
        raise SyncError("issues event payload is missing issue")
    number = issue.get("number")
    if not isinstance(number, int):
        raise SyncError("issues event payload has an invalid issue number")
    return number


def collect_issues(
    event_name: str,
    event_path: Path | None,
    config: SyncConfig,
    github: GitHubClient,
) -> list[GitHubIssue]:
    if event_name == "issues":
        if event_path is None:
            raise SyncError("GITHUB_EVENT_PATH is required for issues events")
        number = _read_event_issue_number(event_path, config.repository)
        return [github.issue(number)]

    milestone_numbers = github.configured_milestone_numbers(config.milestones)
    issues: dict[int, GitHubIssue] = {}
    for title in config.milestones:
        for issue in github.issues_for_milestone(milestone_numbers[title]):
            issues[issue.number] = issue
    return [issues[number] for number in sorted(issues)]


def include_managed_mirror_issues(
    issues: Iterable[GitHubIssue],
    project_issues: Iterable[LinearIssue],
    config: SyncConfig,
    github: GitHubClient,
) -> list[GitHubIssue]:
    collected = {issue.number: issue for issue in issues}
    for linear_issue in project_issues:
        sync_key = description_sync_key(linear_issue.description)
        match = SYNC_KEY_PATTERN.fullmatch(sync_key or "")
        if match is None or match.group("repository") != config.repository:
            continue
        if not is_recoverable_mirror(
            linear_issue, match.group(0), config.mirror_label_id
        ):
            continue
        number = int(match.group("number"))
        if number not in collected:
            collected[number] = github.issue(number)
    return [collected[number] for number in sorted(collected)]


def _parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true", help="report mutations without applying them")
    return parser.parse_args(argv)


def run(argv: list[str] | None = None) -> int:
    args = _parse_args(sys.argv[1:] if argv is None else argv)
    try:
        config = load_config(DEFAULT_CONFIG_PATH)
        github_token = os.environ.get("GITHUB_TOKEN", "").strip()
        linear_api_key = os.environ.get("LINEAR_API_KEY", "").strip()
        if not github_token:
            raise SyncError("GITHUB_TOKEN is required")
        if not linear_api_key:
            raise SyncError("LINEAR_API_KEY is required")

        http = JsonHttpClient()
        github = GitHubClient(github_token, config.repository, http)
        linear = LinearClient(linear_api_key, http)
        event_name = os.environ.get("GITHUB_EVENT_NAME", "workflow_dispatch")
        event_path_value = os.environ.get("GITHUB_EVENT_PATH", "").strip()
        event_path = Path(event_path_value) if event_path_value else None
        issues = collect_issues(event_name, event_path, config, github)
        project_issues: list[LinearIssue] | None = None
        if event_name != "issues":
            project_issues = linear.project_issues(config.project_id)
            issues = include_managed_mirror_issues(
                issues, project_issues, config, github
            )
        engine = SyncEngine(
            config,
            linear,
            dry_run=args.dry_run,
            project_issues=project_issues,
            recover_unmapped_orphans=event_name != "issues",
        )
        for issue in issues:
            print(engine.sync(issue))
        print(f"reconciled {len(issues)} GitHub issue(s)")
        return 0
    except SyncError as error:
        print(f"linear release sync failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(run())
