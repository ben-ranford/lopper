import io
import json
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest import mock
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlparse

from scripts import linear_release_sync as sync


REPOSITORY = "ben-ranford/lopper"
MIRROR_LABEL = "label-mirror"
IMPROVEMENT_LABEL = "label-improvement"
FEATURE_LABEL = "label-feature"
BUG_LABEL = "label-bug"
V2_LABEL = "label-v2"
V3_LABEL = "label-v3"
V2_MILESTONE = "milestone-v2"
V3_MILESTONE = "milestone-v3"
CUSTOM_LABEL = "label-custom"


def test_config():
    return sync.SyncConfig(
        repository=REPOSITORY,
        team_id="team",
        project_id="project",
        open_state_id="backlog",
        closed_state_id="done",
        mirror_label_id=MIRROR_LABEL,
        default_type_label_id=IMPROVEMENT_LABEL,
        type_label_rules=(
            (frozenset({"bug"}), BUG_LABEL),
            (frozenset({"enhancement", "feature"}), FEATURE_LABEL),
        ),
        milestones={
            "v2.0.0": sync.MilestoneMapping(V2_LABEL, V2_MILESTONE),
            "v3.0.0": sync.MilestoneMapping(V3_LABEL, V3_MILESTONE),
        },
    )


def github_issue(
    *,
    number=42,
    title="Ship the release sync",
    body="Detailed delivery context.",
    state="open",
    labels=("enhancement",),
    milestone="v2.0.0",
):
    return sync.GitHubIssue(
        number=number,
        title=title,
        body=body,
        url=f"https://github.com/{REPOSITORY}/issues/{number}",
        state=state,
        labels=labels,
        milestone=milestone,
    )


def linear_issue(
    issue,
    *,
    description=None,
    state_id="backlog",
    milestone_id=V2_MILESTONE,
    labels=None,
):
    return sync.LinearIssue(
        id="linear-id",
        identifier="BEN-42",
        title=f"GH #{issue.number} — {issue.title}",
        description=sync.render_description(issue) if description is None else description,
        state_id=state_id,
        project_id="project",
        project_milestone_id=milestone_id,
        label_ids=frozenset(labels or {MIRROR_LABEL, FEATURE_LABEL, V2_LABEL}),
    )


class FakeLinear:
    def __init__(self, attached=(), project_issues=()):
        self.attached = list(attached)
        self.project_issue_results = list(project_issues)
        self.created = []
        self.updated = []
        self.links = []
        self.identity_lookups = []
        self.identities = {}

    def issues_attached_to(self, url):
        self.last_lookup = url
        return list(self.attached)

    def project_issues(self, project_id):
        self.last_project_lookup = project_id
        return list(self.project_issue_results)

    def create_issue(self, values):
        self.created.append(values)
        return "new-id", "BEN-99"

    def issue_identity(self, issue_id):
        self.identity_lookups.append(issue_id)
        return self.identities[issue_id]

    def update_issue(self, issue_id, values):
        self.updated.append((issue_id, values))

    def attach_url(self, issue_id, issue):
        self.links.append((issue_id, issue.url))


class SyncEngineTests(unittest.TestCase):
    def test_creates_missing_mapped_mirror_and_attachment(self):
        issue = github_issue()
        linear = FakeLinear()

        result = sync.SyncEngine(test_config(), linear).sync(issue)

        self.assertEqual(result, "created GH #42: BEN-99")
        self.assertEqual(linear.last_lookup, issue.url)
        self.assertEqual(linear.links, [("new-id", issue.url)])
        created = linear.created[0]
        self.assertEqual(created["id"], sync.deterministic_issue_id(issue.sync_key))
        self.assertEqual(uuid.UUID(created["id"]).version, 4)
        self.assertEqual(created["teamId"], "team")
        self.assertEqual(created["projectId"], "project")
        self.assertEqual(created["projectMilestoneId"], V2_MILESTONE)
        self.assertEqual(created["stateId"], "backlog")
        self.assertEqual(
            set(created["labelIds"]), {MIRROR_LABEL, FEATURE_LABEL, V2_LABEL}
        )
        self.assertIn(sync._sync_marker(issue.sync_key), created["description"])

    def test_concurrent_create_reuses_deterministic_mirror(self):
        issue = github_issue()
        expected_id = sync.deterministic_issue_id(issue.sync_key)
        linear = FakeLinear()
        linear.create_issue = mock.Mock(
            side_effect=sync.SyncError("Linear GraphQL error: duplicate id")
        )
        linear.identities[expected_id] = (
            expected_id,
            "BEN-99",
            sync.render_description(issue),
        )

        result = sync.SyncEngine(test_config(), linear).sync(issue)

        self.assertEqual(result, "joined concurrent create GH #42: BEN-99")
        self.assertEqual(linear.identity_lookups, [expected_id])
        self.assertEqual(linear.links, [(expected_id, issue.url)])

    def test_create_error_is_preserved_when_deterministic_id_is_not_owned(self):
        issue = github_issue()
        expected_id = sync.deterministic_issue_id(issue.sync_key)
        linear = FakeLinear()
        linear.create_issue = mock.Mock(
            side_effect=sync.SyncError("Linear GraphQL error: permission denied")
        )
        linear.identities[expected_id] = (expected_id, "BEN-99", "Human issue")

        with self.assertRaisesRegex(sync.SyncError, "permission denied"):
            sync.SyncEngine(test_config(), linear).sync(issue)
        self.assertEqual(linear.links, [])

    def test_hand_authored_attached_issue_is_covered_but_untouched(self):
        issue = github_issue()
        linear = FakeLinear([linear_issue(issue, description="Human planning context")])

        result = sync.SyncEngine(test_config(), linear).sync(issue)

        self.assertEqual(result, "covered GH #42: BEN-42 is hand-authored")
        self.assertEqual(linear.created, [])
        self.assertEqual(linear.updated, [])

    def test_recovers_marked_mirror_when_attachment_was_not_created(self):
        issue = github_issue()
        orphan = linear_issue(issue)
        linear = FakeLinear(project_issues=[orphan])

        result = sync.SyncEngine(test_config(), linear).sync(issue)

        self.assertIn("recovered GH #42: BEN-42", result)
        self.assertEqual(linear.last_project_lookup, "project")
        self.assertEqual(linear.links, [("linear-id", issue.url)])
        self.assertEqual(linear.created, [])

    def test_orphan_recovery_ignores_copied_key_without_ownership_marker(self):
        issue = github_issue()
        copied_key = f"**Sync key:** `{issue.sync_key}`"
        planning_issue = linear_issue(
            issue,
            description=copied_key,
            labels={FEATURE_LABEL},
        )
        linear = FakeLinear(project_issues=[planning_issue])

        result = sync.SyncEngine(test_config(), linear).sync(issue)

        self.assertEqual(result, "created GH #42: BEN-99")
        self.assertEqual(linear.updated, [])
        self.assertEqual(linear.links, [("new-id", issue.url)])

    def test_legacy_sync_key_marks_existing_mirror_as_owned(self):
        issue = github_issue(title="New title")
        legacy = f"**Sync key:** `{issue.sync_key}`"
        current = linear_issue(issue, description=legacy)
        current = sync.LinearIssue(**{**current.__dict__, "title": "Old title"})
        linear = FakeLinear([current])

        sync.SyncEngine(test_config(), linear).sync(issue)

        self.assertEqual(linear.updated[0][1]["title"], "GH #42 — New title")

    def test_close_and_reopen_follow_github_state(self):
        cases = (("closed", "backlog", "done"), ("open", "done", "backlog"))
        for state, current_state, expected_state in cases:
            with self.subTest(state=state):
                issue = github_issue(state=state)
                current = linear_issue(issue, state_id=current_state)
                linear = FakeLinear([current])

                sync.SyncEngine(test_config(), linear).sync(issue)

                self.assertEqual(linear.updated[0][1]["stateId"], expected_state)

    def test_release_change_preserves_unmanaged_labels(self):
        issue = github_issue(milestone="v3.0.0")
        current = linear_issue(
            issue,
            milestone_id=V2_MILESTONE,
            labels={MIRROR_LABEL, FEATURE_LABEL, V2_LABEL, CUSTOM_LABEL},
        )
        linear = FakeLinear([current])

        sync.SyncEngine(test_config(), linear).sync(issue)

        changes = linear.updated[0][1]
        self.assertEqual(changes["projectMilestoneId"], V3_MILESTONE)
        self.assertEqual(
            set(changes["labelIds"]),
            {MIRROR_LABEL, FEATURE_LABEL, V3_LABEL, CUSTOM_LABEL},
        )

    def test_demilestoned_issue_does_not_create_and_clears_managed_release(self):
        issue = github_issue(milestone=None)
        linear = FakeLinear()

        self.assertEqual(
            sync.SyncEngine(test_config(), linear).sync(issue),
            "skip GH #42: milestone is not mapped",
        )
        self.assertEqual(linear.created, [])

        owned = linear_issue(
            issue,
            milestone_id=V2_MILESTONE,
            labels={MIRROR_LABEL, FEATURE_LABEL, V2_LABEL, CUSTOM_LABEL},
        )
        linear = FakeLinear([owned])
        sync.SyncEngine(test_config(), linear).sync(issue)
        changes = linear.updated[0][1]
        self.assertIsNone(changes["projectMilestoneId"])
        self.assertEqual(
            set(changes["labelIds"]), {MIRROR_LABEL, FEATURE_LABEL, CUSTOM_LABEL}
        )

    def test_unmapped_issue_event_skips_without_project_scan(self):
        issue = github_issue(milestone=None)
        linear = FakeLinear()

        result = sync.SyncEngine(
            test_config(), linear, recover_unmapped_orphans=False
        ).sync(issue)

        self.assertEqual(result, "skip GH #42: milestone is not mapped")
        self.assertFalse(hasattr(linear, "last_project_lookup"))

    def test_duplicate_url_attachments_fail_without_mutation(self):
        issue = github_issue()
        first = linear_issue(issue)
        second = sync.LinearIssue(**{**first.__dict__, "id": "other", "identifier": "BEN-43"})
        linear = FakeLinear([first, second])
        engine = sync.SyncEngine(test_config(), linear)

        with self.assertRaisesRegex(sync.SyncError, "multiple Linear issues"):
            engine.sync(issue)
        self.assertEqual(linear.created, [])
        self.assertEqual(linear.updated, [])

    def test_dry_run_reports_create_and_update_without_mutation(self):
        missing = FakeLinear()
        result = sync.SyncEngine(test_config(), missing, dry_run=True).sync(github_issue())
        self.assertEqual(result, "dry-run create GH #42")
        self.assertEqual(missing.created, [])

        issue = github_issue(state="closed")
        current = linear_issue(issue, state_id="backlog")
        existing = FakeLinear([current])
        result = sync.SyncEngine(test_config(), existing, dry_run=True).sync(issue)
        self.assertIn("dry-run update GH #42", result)
        self.assertEqual(existing.updated, [])

    def test_unchanged_mirror_does_not_write(self):
        issue = github_issue()
        linear = FakeLinear([linear_issue(issue)])

        result = sync.SyncEngine(test_config(), linear).sync(issue)

        self.assertEqual(result, "unchanged GH #42: BEN-42")
        self.assertEqual(linear.updated, [])

    def test_type_label_rules_are_case_insensitive_and_default_to_improvement(self):
        config = test_config()
        self.assertEqual(config.type_label_for(["BUG"]), BUG_LABEL)
        self.assertEqual(config.type_label_for(["Enhancement"]), FEATURE_LABEL)
        self.assertEqual(config.type_label_for(["documentation"]), IMPROVEMENT_LABEL)

    def test_linear_title_is_bounded_without_losing_github_identity(self):
        title = sync.render_title(github_issue(title="a" * 300))
        self.assertEqual(len(title), sync.LINEAR_TITLE_LIMIT)
        self.assertTrue(title.startswith("GH #42 — "))
        self.assertTrue(title.endswith("…"))


class FakeGitHubHttp:
    def __init__(self):
        self.calls = []

    def request(self, method, url, **_kwargs):
        self.calls.append((method, url))
        parsed = urlparse(url)
        query = parse_qs(parsed.query)
        page = int(query["page"][0])
        if parsed.path.endswith("/milestones"):
            if page == 1:
                return [
                    {"number": index, "title": f"release-{index}"}
                    for index in range(1, 101)
                ]
            return [{"number": 101, "title": "v2.0.0"}]
        if page == 1:
            issues = [raw_github_issue(index) for index in range(1, 101)]
            issues[0]["pull_request"] = {"url": "ignored"}
            return issues
        return [raw_github_issue(101)]


def raw_github_issue(number=42, milestone="v2.0.0"):
    return {
        "number": number,
        "title": f"Issue {number}",
        "body": "Body",
        "html_url": f"https://github.com/{REPOSITORY}/issues/{number}",
        "state": "open",
        "labels": [{"name": "enhancement"}],
        "milestone": {"title": milestone} if milestone else None,
    }


class ClientAndCollectionTests(unittest.TestCase):
    def test_github_pagination_and_pull_request_filtering(self):
        http = FakeGitHubHttp()
        client = sync.GitHubClient("token", REPOSITORY, http)

        numbers = client.configured_milestone_numbers(["v2.0.0"])
        issues = client.issues_for_milestone(numbers["v2.0.0"])

        self.assertEqual(numbers, {"v2.0.0": 101})
        self.assertEqual(len(issues), 100)
        self.assertNotIn(1, {issue.number for issue in issues})
        self.assertIn(101, {issue.number for issue in issues})
        self.assertEqual(len(http.calls), 4)

    def test_issue_event_refetches_current_github_issue(self):
        stale = raw_github_issue(milestone="v2.0.0")
        current = github_issue(milestone="v3.0.0")
        payload = {"repository": {"full_name": REPOSITORY}, "issue": stale}
        github = mock.Mock(spec=sync.GitHubClient)
        github.issue.return_value = current
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "event.json")
            path.write_text(json.dumps(payload), encoding="utf-8")
            issues = sync.collect_issues(
                "issues", path, test_config(), github
            )
        self.assertEqual(issues, [current])
        self.assertEqual(issues[0].milestone, "v3.0.0")
        github.issue.assert_called_once_with(42)

    def test_issue_event_rejects_wrong_repository(self):
        payload = {"repository": {"full_name": "other/repo"}, "issue": raw_github_issue()}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "event.json")
            path.write_text(json.dumps(payload), encoding="utf-8")
            config = test_config()
            github = mock.Mock(spec=sync.GitHubClient)
            with self.assertRaisesRegex(sync.SyncError, "does not match"):
                sync.collect_issues("issues", path, config, github)

    def test_full_reconcile_includes_marked_mirror_no_longer_in_release(self):
        mapped = github_issue(number=10)
        demilestoned = github_issue(number=11, milestone=None)
        mirror = linear_issue(demilestoned)
        github = mock.Mock(spec=sync.GitHubClient)
        github.issue.return_value = demilestoned

        issues = sync.include_managed_mirror_issues(
            [mapped], [mirror], test_config(), github
        )

        self.assertEqual([issue.number for issue in issues], [10, 11])
        github.issue.assert_called_once_with(11)

    def test_full_reconcile_ignores_markers_for_other_repositories(self):
        issue = github_issue()
        description = "<!-- github-linear-release-sync:github:other/repo#99 -->"
        mirror = linear_issue(issue, description=description)
        github = mock.Mock(spec=sync.GitHubClient)

        issues = sync.include_managed_mirror_issues(
            [issue], [mirror], test_config(), github
        )

        self.assertEqual(issues, [issue])
        github.issue.assert_not_called()

    def test_full_reconcile_ignores_copied_key_without_ownership_marker(self):
        issue = github_issue()
        copied_key = f"**Sync key:** `{issue.sync_key}`"
        planning_issue = linear_issue(
            issue,
            description=copied_key,
            labels={FEATURE_LABEL},
        )
        github = mock.Mock(spec=sync.GitHubClient)

        issues = sync.include_managed_mirror_issues(
            [], [planning_issue], test_config(), github
        )

        self.assertEqual(issues, [])
        github.issue.assert_not_called()

    def test_linear_graphql_errors_are_fatal_even_on_http_success(self):
        http = mock.Mock()
        http.request.return_value = {"errors": [{"message": "permission denied"}]}
        client = sync.LinearClient("key", http)

        with self.assertRaisesRegex(sync.SyncError, "permission denied"):
            client.issues_attached_to("https://github.com/example/repo/issues/1")

    def test_linear_issue_identity_recovers_a_concurrently_created_mirror(self):
        issue = github_issue()
        issue_id = sync.deterministic_issue_id(issue.sync_key)
        http = mock.Mock()
        http.request.return_value = {
            "data": {
                "issue": {
                    "id": issue_id,
                    "identifier": "BEN-99",
                    "description": sync.render_description(issue),
                }
            }
        }

        identity = sync.LinearClient("key", http).issue_identity(issue_id)

        self.assertEqual(
            identity, (issue_id, "BEN-99", sync.render_description(issue))
        )
        variables = http.request.call_args.kwargs["payload"]["variables"]
        self.assertEqual(variables, {"id": issue_id})

    def test_linear_url_lookup_deduplicates_multiple_attachments_on_same_issue(self):
        raw = {
            "id": "id",
            "identifier": "BEN-1",
            "title": "Title",
            "description": "Description",
            "state": {"id": "backlog"},
            "project": {"id": "project"},
            "projectMilestone": {"id": "milestone"},
            "labels": {"nodes": [{"id": "label"}]},
        }
        http = mock.Mock()
        http.request.return_value = {
            "data": {
                "attachmentsForURL": {"nodes": [{"issue": raw}, {"issue": raw}]}
            }
        }

        issues = sync.LinearClient("key", http).issues_attached_to("https://example.test")

        self.assertEqual(len(issues), 1)
        self.assertEqual(issues[0].identifier, "BEN-1")

    def test_linear_url_lookup_paginates_all_issue_labels(self):
        raw = {
            "id": "id",
            "identifier": "BEN-1",
            "title": "Title",
            "description": "Description",
            "state": {"id": "backlog"},
            "project": {"id": "project"},
            "projectMilestone": None,
            "labels": {
                "nodes": [{"id": "first-label"}],
                "pageInfo": {"hasNextPage": True, "endCursor": "labels-next"},
            },
        }

        def response(*_args, **kwargs):
            query = kwargs["payload"]["query"]
            if "query IssuesAttachedToURL" in query:
                return {
                    "data": {"attachmentsForURL": {"nodes": [{"issue": raw}]}}
                }
            self.assertIn("query IssueLabels", query)
            self.assertEqual(kwargs["payload"]["variables"]["after"], "labels-next")
            return {
                "data": {
                    "issue": {
                        "labels": {
                            "nodes": [{"id": "last-label"}],
                            "pageInfo": {"hasNextPage": False, "endCursor": None},
                        }
                    }
                }
            }

        http = mock.Mock()
        http.request.side_effect = response

        issues = sync.LinearClient("key", http).issues_attached_to(
            "https://example.test"
        )

        self.assertEqual(
            issues[0].label_ids, frozenset({"first-label", "last-label"})
        )
        self.assertEqual(http.request.call_count, 2)

    def test_linear_project_lookup_paginates_all_issue_labels(self):
        raw = {
            "id": "id",
            "identifier": "BEN-1",
            "title": "Title",
            "description": "Description",
            "state": {"id": "backlog"},
            "project": {"id": "project"},
            "projectMilestone": None,
            "labels": {
                "nodes": [{"id": "first-label"}],
                "pageInfo": {"hasNextPage": True, "endCursor": "labels-next"},
            },
        }

        def response(*_args, **kwargs):
            query = kwargs["payload"]["query"]
            if "query ProjectIssues" in query:
                return {
                    "data": {
                        "project": {
                            "issues": {
                                "nodes": [raw],
                                "pageInfo": {
                                    "hasNextPage": False,
                                    "endCursor": None,
                                },
                            }
                        }
                    }
                }
            self.assertIn("query IssueLabels", query)
            return {
                "data": {
                    "issue": {
                        "labels": {
                            "nodes": [{"id": "last-label"}],
                            "pageInfo": {"hasNextPage": False, "endCursor": None},
                        }
                    }
                }
            }

        http = mock.Mock()
        http.request.side_effect = response

        issues = sync.LinearClient("key", http).project_issues("project")

        self.assertEqual(
            issues[0].label_ids, frozenset({"first-label", "last-label"})
        )
        self.assertEqual(http.request.call_count, 2)

    def test_linear_project_issue_pagination_recovers_all_pages(self):
        def response(*_args, **kwargs):
            after = kwargs["payload"]["variables"]["after"]
            raw = {
                "id": "first" if after is None else "second",
                "identifier": "BEN-1" if after is None else "BEN-2",
                "title": "Title",
                "description": "Description",
                "state": {"id": "backlog"},
                "project": {"id": "project"},
                "projectMilestone": None,
                "labels": {"nodes": []},
            }
            return {
                "data": {
                    "project": {
                        "issues": {
                            "nodes": [raw],
                            "pageInfo": {
                                "hasNextPage": after is None,
                                "endCursor": "next" if after is None else None,
                            },
                        }
                    }
                }
            }

        http = mock.Mock()
        http.request.side_effect = response

        issues = sync.LinearClient("key", http).project_issues("project")

        self.assertEqual([issue.identifier for issue in issues], ["BEN-1", "BEN-2"])
        self.assertEqual(http.request.call_count, 2)

    def test_http_and_network_failures_have_actionable_messages(self):
        client = sync.JsonHttpClient()
        error = HTTPError(
            "https://api.example.test",
            503,
            "Unavailable",
            {},
            io.BytesIO(b"try later"),
        )
        with mock.patch.object(sync, "urlopen", side_effect=error):
            with self.assertRaisesRegex(sync.SyncError, "HTTP 503: try later"):
                client.request("GET", "https://api.example.test")

        with mock.patch.object(sync, "urlopen", side_effect=URLError("offline")):
            with self.assertRaisesRegex(sync.SyncError, "offline"):
                client.request("GET", "https://api.example.test")

    def test_checked_in_config_loads_expected_release_targets(self):
        config = sync.load_config(Path(".github/linear-release-sync.json"))
        self.assertEqual(config.repository, REPOSITORY)
        self.assertEqual(set(config.milestones), {"v2.0.0", "v3.0.0", "v4.0.0"})
        self.assertEqual(
            config.milestones["v2.0.0"].project_milestone_id,
            "619c7f94-ecff-4737-92ea-8fe065047b6a",
        )

    def test_config_path_cannot_escape_workspace(self):
        outside = Path("/etc/hosts")
        with self.assertRaisesRegex(sync.SyncError, "must stay within workspace"):
            sync.load_config(outside)


class RunTests(unittest.TestCase):
    def test_missing_credentials_fail_before_network_access(self):
        with mock.patch.dict(sync.os.environ, {}, clear=True):
            with mock.patch("sys.stderr", new_callable=io.StringIO) as stderr:
                result = sync.run([])
        self.assertEqual(result, 1)
        self.assertIn("GITHUB_TOKEN is required", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
