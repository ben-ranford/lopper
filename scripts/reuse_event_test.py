#!/usr/bin/env python3
"""Adversarial event, candidate-tree, artifact and status-publication tests."""

import copy
import http.client
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import urllib.request
import urllib.response
import zipfile

import reuse_event as event
import reuse_policy_review as policy


REPOSITORY = "ben-ranford/lopper"
OWNER = "ben-ranford"
BASE = "a" * 40
HEAD = "b" * 40
SIGNAL = ".github/workflows/reuse-review-signal.yml"
PREFIX = "/repos/" + REPOSITORY
SNAPSHOT = dict(version=1, repository=REPOSITORY, repository_id=10,
                head_repository_id=20, pull_number=12, base=BASE, head=HEAD, base_ref="main")
CI_RUNS = PREFIX + "/actions/workflows/ci.yml/runs?per_page=100&page=1&head_sha=" + HEAD + "&event=pull_request"


def pull_document():
    return dict(number=12, state="open", base=dict(sha=BASE, ref="main",
                repo=dict(id=10, full_name=REPOSITORY)),
                head=dict(sha=HEAD, repo=dict(id=20)))


def signal_document():
    return dict(id=7, run_attempt=2, status="completed", event="pull_request_review",
                path=SIGNAL, name="Reuse review signal", actor=dict(login=OWNER),
                repository=dict(id=10), head_repository=dict(id=20), head_sha=HEAD,
                pull_requests=[pull_document()])


def review_document():
    return dict(id=19, user=dict(login=OWNER), state="COMMENTED", commit_id=HEAD,
                submitted_at="2026-09-30T12:00:00Z",
                pull_request_url=f"https://api.github.com/repos/{REPOSITORY}/pulls/12",
                body=policy.format_signoff(repository=REPOSITORY, pull_request=12,
                                          base=BASE, head=HEAD, decision="approve"))


def result_document(paths=None):
    return dict(version=1, snapshot=copy.deepcopy(SNAPSHOT), candidate="c" * 40,
                detector_exit=0, policy_paths=paths or [])


def suppression_document():
    return dict(headSHA=HEAD, baseSHA=BASE, runId=41, runAttempt=2, artifactId=101, suppressionCount=0)


def ci_document():
    return dict(id=41, workflow_id=6, run_attempt=2, path=event.CI_WORKFLOW, name="ci",
                event="pull_request", status="completed", conclusion="success", head_sha=HEAD,
                repository=dict(id=10), head_repository=dict(id=20), pull_requests=[pull_document()],
                run_started_at="2026-09-30T12:00:00Z", updated_at="2026-09-30T12:10:00Z")


def suppression_artifact():
    return dict(id=101, name="pr-report-inputs-12", expired=False, size_in_bytes=100,
                created_at="2026-09-30T12:05:00Z", digest="sha256:" + "a" * 64,
                workflow_run=dict(id=41, head_sha=HEAD, repository_id=10, head_repository_id=20))


def archive(body=b'{"pull_number":12}', name="reuse-review-wakeup.json", mode=0o100600):
    data = io.BytesIO()
    with zipfile.ZipFile(data, "w") as zipped:
        info = zipfile.ZipInfo(name)
        info.external_attr = mode << 16
        zipped.writestr(info, body)
    return data.getvalue()


def unusual_archive(flags=0, compression=0):
    data = bytearray(archive())
    central = data.index(b"PK\x01\x02")
    struct.pack_into("<HH", data, 6, flags, compression)
    struct.pack_into("<HH", data, central + 8, flags, compression)
    return bytes(data)


class FakeAPI:
    token = "test-token"

    def __init__(self):
        self.responses = {
            PREFIX: dict(id=10, full_name=REPOSITORY, default_branch="main", owner=dict(login=OWNER)),
            PREFIX + "/pulls/12": pull_document(),
            PREFIX + "/actions/runs/7": signal_document(),
            PREFIX + "/git/ref/heads/main": dict(object=dict(sha=BASE)),
            PREFIX + "/actions/runs/7/artifacts?per_page=100&page=1": dict(artifacts=[
                dict(id=99, name="reuse-review-wakeup-7-2", expired=False, size_in_bytes=200)]),
            PREFIX + "/actions/workflows/ci.yml": dict(id=6, path=event.CI_WORKFLOW, name="ci"),
            PREFIX + "/actions/runs/41": ci_document(),
            CI_RUNS: dict(total_count=1, workflow_runs=[ci_document()]),
            PREFIX + "/actions/artifacts/101": suppression_artifact(),
        }
        self.page_responses = {
            PREFIX + "/pulls/12/reviews": [review_document()],
            PREFIX + "/commits/" + HEAD + "/pulls": [],
            PREFIX + "/pulls": [pull_document()],
        }
        self.posts = []
        self.archive = archive()

    def request(self, path, data=None):
        if data is not None:
            self.posts.append((path, copy.deepcopy(data)))
            return {}
        return copy.deepcopy(self.responses[path])

    def pages(self, path):
        return copy.deepcopy(self.page_responses[path])

    def artifact_archive(self, repository, artifact_id):
        if (repository, artifact_id) != (REPOSITORY, 99):
            raise AssertionError("Wrong artifact identity")
        return self.archive


class EventTests(unittest.TestCase):
    def setUp(self):
        self.api = FakeAPI()
        self.payload = dict(repository=dict(id=10), action="synchronize", number=12,
                            pull_request=pull_document())

    def prepare(self, payload=None, name="pull_request_target"):
        return event.prepare(self.api, payload or self.payload, name, REPOSITORY, 10, SIGNAL, OWNER)

    def test_fork_pr_binds_both_repositories_and_exact_revisions_before_pending(self):
        self.assertEqual(self.prepare(), SNAPSHOT)
        self.assertEqual(self.api.posts[-1][0], PREFIX + "/statuses/" + HEAD)
        self.assertEqual(self.api.posts[-1][1]["state"], "pending")
        self.assertEqual(self.api.posts[-1][1]["context"], "reuse-check")

    def test_stale_or_other_repository_event_never_posts(self):
        for field in ("head", "base", "repository", "head_repository"):
            payload = copy.deepcopy(self.payload)
            if field in ("head", "base"):
                payload["pull_request"][field]["sha"] = "c" * 40
            elif field == "repository":
                payload["repository"]["id"] = 99
            else:
                payload["pull_request"]["head"]["repo"]["id"] = 99
            with self.subTest(field=field), self.assertRaises(event.EventError):
                self.prepare(payload)
        self.assertEqual(self.api.posts, [])

    def test_stale_pr_base_or_malformed_current_target_never_publishes_pending(self):
        for target in ("c" * 40, "main", "A" * 40, None):
            self.api.responses[PREFIX + "/git/ref/heads/main"]["object"]["sha"] = target
            with self.subTest(target=target):
                self.assertEqual(self.payload["pull_request"]["base"]["sha"], BASE)
                self.assertEqual(self.api.responses[PREFIX + "/pulls/12"]["base"]["sha"], BASE)
                with self.assertRaises(ValueError):
                    self.prepare()
                self.assertEqual(self.api.posts, [])

    def test_closed_or_nondefault_target_pr_is_rejected(self):
        self.api.responses[PREFIX + "/pulls/12"]["base"]["ref"] = "other"
        with self.assertRaises(event.EventError):
            self.prepare()
        self.api.responses[PREFIX + "/pulls/12"] = dict(pull_document(), state="closed")
        with self.assertRaises(event.EventError):
            self.prepare()

    def test_signal_is_only_a_wakeup_even_when_source_conclusion_failed(self):
        signal = dict(repository=dict(id=10), action="completed", workflow_run=dict(id=7))
        self.api.responses[PREFIX + "/actions/runs/7"]["conclusion"] = "failure"
        self.assertEqual(self.prepare(signal, "workflow_run"), SNAPSHOT)
        self.assertEqual(self.api.posts[-1][1]["state"], "pending")

    def test_signal_rejects_wrong_actor_workflow_or_repository(self):
        signal = dict(repository=dict(id=10), action="completed", workflow_run=dict(id=7))
        for key, value in (("actor", dict(login="attacker")), ("path", "other.yml"),
                           ("event", "push"), ("repository", dict(id=99)),
                           ("status", "in_progress"), ("name", "Other")):
            self.api.responses[PREFIX + "/actions/runs/7"] = dict(signal_document(), **{key: value})
            with self.subTest(key=key), self.assertRaises(event.EventError):
                self.prepare(signal, "workflow_run")
        self.assertEqual(self.api.posts, [])

    def test_ci_wakeups_only_invalidate_and_bind_live_run_identity(self):
        for action in ("requested", "in_progress", "completed"):
            api = FakeAPI()
            api.responses[PREFIX + "/actions/runs/41"]["conclusion"] = "failure"
            payload = dict(repository=dict(id=10), action=action, workflow_run=dict(id=41, path="attacker.yml"))
            with self.subTest(action=action):
                snapshot = event.prepare(api, payload, "workflow_run", REPOSITORY, 10, SIGNAL, OWNER)
                self.assertEqual(snapshot, SNAPSHOT)
                self.assertEqual([data["state"] for _, data in api.posts], ["pending"])

    def test_spoofed_ci_identity_never_changes_status(self):
        payload = dict(repository=dict(id=10), action="completed", workflow_run=dict(id=41))
        for field, value in (("workflow_id", 99), ("path", "attacker.yml"), ("name", "other"),
                             ("event", "push"), ("repository", dict(id=99)), ("id", 42)):
            self.api = FakeAPI()
            self.api.responses[PREFIX + "/actions/runs/41"][field] = value
            with self.subTest(field=field), self.assertRaises(event.EventError):
                self.prepare(payload, "workflow_run")
            self.assertEqual(self.api.posts, [])

    def test_unresolvable_authenticated_ci_wakeup_erases_previous_success(self):
        payload = dict(repository=dict(id=10), action="in_progress", workflow_run=dict(id=41))
        for scenario in ("missing", "malformed", "duplicate", "old_base", "old_head", "other_head_repository"):
            self.api = FakeAPI()
            run = self.api.responses[PREFIX + "/actions/runs/41"]
            if scenario == "missing":
                run["pull_requests"] = []
            elif scenario == "malformed":
                run["pull_requests"] = None
            elif scenario == "duplicate":
                run["pull_requests"].append(pull_document())
            elif scenario == "other_head_repository":
                run["pull_requests"][0]["head"]["repo"]["id"] = 99
            else:
                part = "base" if scenario == "old_base" else "head"
                run["pull_requests"][0][part]["sha"] = "c" * 40
            event.status(self.api, SNAPSHOT, "success", "Previously passed")
            with self.subTest(scenario=scenario), self.assertRaises(event.EventError):
                self.prepare(payload, "workflow_run")
            self.assertEqual([data["state"] for _, data in self.api.posts], ["success", "pending"])
            self.assertIn("CI wakeup unresolved", self.api.posts[-1][1]["description"])

    def test_missing_fork_association_uses_bounded_attempt_artifact(self):
        signal = dict(repository=dict(id=10), action="completed", workflow_run=dict(id=7))
        self.api.responses[PREFIX + "/actions/runs/7"]["pull_requests"] = []
        self.assertEqual(self.prepare(signal, "workflow_run"), SNAPSHOT)
        self.assertEqual(self.api.posts[-1][1]["state"], "pending")

    def test_signal_artifact_cannot_change_live_head_repository_or_grant_success(self):
        signal = dict(repository=dict(id=10), action="completed", workflow_run=dict(id=7))
        self.api.responses[PREFIX + "/actions/runs/7"]["pull_requests"] = []
        self.api.responses[PREFIX + "/pulls/12"]["head"]["repo"]["id"] = 999
        with self.assertRaises(event.EventError):
            self.prepare(signal, "workflow_run")
        self.assertEqual(self.api.posts[-1][1]["state"], "pending")

    def test_missing_or_malformed_owner_wakeup_invalidates_before_failing(self):
        signal = dict(repository=dict(id=10), action="completed", workflow_run=dict(id=7))
        for scenario in ("missing", "malformed", "encrypted", "compression"):
            self.api = FakeAPI()
            self.api.responses[PREFIX + "/actions/runs/7"]["pull_requests"] = []
            if scenario == "missing":
                self.api.responses[PREFIX + "/actions/runs/7/artifacts?per_page=100&page=1"]["artifacts"] = []
            elif scenario == "malformed":
                self.api.archive = b"not a zip"
            else:
                self.api.archive = unusual_archive(flags=1) if scenario == "encrypted" else unusual_archive(compression=99)
            with self.subTest(scenario=scenario), self.assertRaises(event.EventError):
                self.prepare(signal, "workflow_run")
            self.assertEqual(self.api.posts[-1][0], PREFIX + "/statuses/" + HEAD)
            self.assertEqual(self.api.posts[-1][1]["state"], "pending")
            self.assertIn("Review wakeup unresolved", self.api.posts[-1][1]["description"])

    def test_owner_refresh_fetches_live_comment_and_never_accepts_comment_as_signoff(self):
        payload = dict(repository=dict(id=10), action="created", sender=dict(login=OWNER),
                       issue=dict(number=12, pull_request={"url": "unused"}), comment=dict(id=81))
        self.api.responses[PREFIX + "/issues/comments/81"] = dict(
            user=dict(login=OWNER), body="/reuse-check", issue_url=f"https://api.github.com/repos/{REPOSITORY}/issues/12")
        self.assertEqual(self.prepare(payload, "issue_comment"), SNAPSHOT)
        self.api.responses[PREFIX + "/issues/comments/81"]["body"] = "edited away"
        with self.assertRaises(event.EventError):
            self.prepare(payload, "issue_comment")

    def test_merge_group_exact_parser_exists_but_dispatch_is_inactive(self):
        payload = dict(repository=dict(id=10), merge_group=dict(base_sha=BASE, head_sha=HEAD))
        self.assertEqual(event.merge_group_pair(payload), (BASE, HEAD))
        with self.assertRaises(event.EventError):
            self.prepare(payload, "merge_group")
        payload["merge_group"]["head_sha"] = "main"
        with self.assertRaises(policy.PolicyReviewError):
            event.merge_group_pair(payload)

    def test_base_push_invalidates_each_current_open_default_branch_head(self):
        payload = dict(repository=dict(id=10), ref="refs/heads/main", after="d" * 40)
        self.assertEqual(event.invalidate_base(self.api, payload, "push", REPOSITORY, 10), 1)
        self.assertEqual(self.api.posts[-1][1]["state"], "pending")
        self.assertEqual(self.api.posts[-1][0], PREFIX + "/statuses/" + HEAD)
        payload["ref"] = "refs/heads/attacker"
        with self.assertRaises(event.EventError):
            event.invalidate_base(self.api, payload, "push", REPOSITORY, 10)

    def test_base_invalidation_erases_success_during_pr_metadata_lag(self):
        event.status(self.api, SNAPSHOT, "success", "Previously reviewed exact revision")
        self.api.responses[PREFIX + "/git/ref/heads/main"]["object"]["sha"] = "c" * 40
        self.assertEqual(self.api.responses[PREFIX + "/pulls/12"]["base"]["sha"], BASE)
        self.assertEqual(event.invalidate_pulls(self.api, REPOSITORY, 10, "main"), 1)
        self.assertEqual(self.api.posts[-1][0], PREFIX + "/statuses/" + HEAD)
        self.assertEqual([payload["state"] for _, payload in self.api.posts], ["success", "pending"])
        with self.assertRaises(event.EventError):
            self.prepare()
        self.assertEqual([payload["state"] for _, payload in self.api.posts], ["success", "pending"])

    def test_base_invalidation_continues_after_closed_or_broken_pr(self):
        payload = dict(repository=dict(id=10), ref="refs/heads/main", after=BASE)
        for scenario in ("closed", "retargeted", "broken"):
            api = FakeAPI()
            second = pull_document()
            second["number"] = 13
            second["head"]["sha"] = "e" * 40
            api.page_responses[PREFIX + "/pulls"].append(second)
            api.responses[PREFIX + "/pulls/13"] = second
            first = api.responses[PREFIX + "/pulls/12"]
            if scenario == "closed":
                first["state"] = "closed"
            elif scenario == "retargeted":
                first["base"]["ref"] = "other"
            else:
                first["head"]["repo"] = None
            with self.subTest(scenario=scenario):
                if scenario == "broken":
                    with self.assertRaises(event.EventError):
                        event.invalidate_base(api, payload, "push", REPOSITORY, 10)
                else:
                    self.assertEqual(event.invalidate_base(api, payload, "push", REPOSITORY, 10), 1)
                self.assertEqual(api.posts[-1][0], PREFIX + "/statuses/" + "e" * 40)


class ReviewDismissalTests(unittest.TestCase):
    def setUp(self):
        self.api = FakeAPI()
        self.api.responses[PREFIX + "/actions/runs/7"]["actor"]["login"] = "other-maintainer"
        self.api.page_responses[PREFIX + "/pulls/12/reviews"][0]["state"] = "DISMISSED"
        self.signal = dict(repository=dict(id=10), action="completed", workflow_run=dict(id=7))

    def prepare(self):
        return event.prepare(self.api, self.signal, "workflow_run", REPOSITORY, 10, SIGNAL, OWNER)

    def test_other_maintainer_dismissal_erases_success_without_permission_dependency(self):
        event.status(self.api, SNAPSHOT, "success", "Previously approved")
        self.assertEqual(self.prepare(), SNAPSHOT)
        self.assertEqual([data["state"] for _, data in self.api.posts], ["success", "pending"])
        # Even a successful analysis cannot reinterpret the wakeup as approval.
        with self.assertRaises(event.EventError):
            event.publish(self.api, SNAPSHOT, result_document(["Makefile"]), "success", [OWNER],
                          suppression_document(), "success")
        self.assertEqual(self.api.posts[-1][1]["state"], "failure")

    def test_stale_run_and_missing_association_still_recheck_current_owner_state(self):
        run = self.api.responses[PREFIX + "/actions/runs/7"]
        run["head_sha"] = "c" * 40
        run["pull_requests"] = []
        self.api.page_responses[PREFIX + "/commits/" + "c" * 40 + "/pulls"] = []
        self.assertEqual(self.prepare(), SNAPSHOT)
        self.assertEqual(self.api.posts[-1][0], PREFIX + "/statuses/" + HEAD)

    def test_untrusted_actor_can_only_wake_an_existing_dismissal_despite_pending_draft(self):
        self.api.responses[PREFIX + "/actions/runs/7"]["actor"]["login"] = "ordinary-fork-author"
        self.api.page_responses[PREFIX + "/pulls/12/reviews"].append(
            dict(review_document(), id=20, state="PENDING", submitted_at=None))
        self.assertEqual(self.prepare(), SNAPSHOT)
        self.assertEqual([data["state"] for _, data in self.api.posts], ["pending"])

    def test_nonowner_cannot_invalidate_without_live_owner_dismissal(self):
        cases = ([review_document()], [], [dict(review_document(), user=dict(login="other-maintainer"), state="DISMISSED")],
                 [dict(review_document(), state="PENDING", submitted_at=None)],
                 [dict(review_document(), body="edited away")],
                 [dict(review_document(), commit_id="c" * 40)],
                 [dict(review_document(), state="DISMISSED"),
                  dict(review_document(), id=20, submitted_at="2026-09-30T12:01:00Z")])
        for reviews in cases:
            self.api.page_responses[PREFIX + "/pulls/12/reviews"] = reviews
            with self.subTest(reviews=reviews), self.assertRaises(event.EventError):
                self.prepare()
            self.assertEqual(self.api.posts, [])

    def add_approved_pull(self):
        pull = dict(pull_document(), number=13, head=dict(sha="e" * 40, repo=dict(id=20)))
        self.api.responses[PREFIX + "/pulls/13"] = pull
        self.api.page_responses[PREFIX + "/pulls"].append(pull)
        review = dict(review_document(), commit_id="e" * 40,
                      pull_request_url=f"https://api.github.com/repos/{REPOSITORY}/pulls/13",
                      body=policy.format_signoff(repository=REPOSITORY, pull_request=13,
                                                base=BASE, head="e" * 40, decision="approve"))
        self.api.page_responses[PREFIX + "/pulls/13/reviews"] = [review]

    def test_unresolved_nonowner_wakeup_invalidates_only_live_dismissed_owner_reviews(self):
        self.add_approved_pull()
        run = self.api.responses[PREFIX + "/actions/runs/7"]
        for associations in (None, [], [{"number": 12}]):
            run["pull_requests"] = associations
            self.api.archive = b"unreadable"
            self.api.posts.clear()
            with self.subTest(associations=associations), self.assertRaises(event.EventError):
                self.prepare()
            self.assertEqual([path for path, _ in self.api.posts], [PREFIX + "/statuses/" + HEAD])
            self.assertEqual(self.api.posts[0][1]["state"], "pending")

    def test_unresolved_fallback_continues_after_one_unreadable_review_history(self):
        self.add_approved_pull()
        self.api.responses[PREFIX + "/actions/runs/7"]["pull_requests"] = None
        self.api.page_responses[PREFIX + "/pulls/13/reviews"][0]["state"] = "DISMISSED"
        pages = self.api.pages

        def unavailable_first_review(path):
            if path == PREFIX + "/pulls/12/reviews":
                raise TimeoutError("unavailable")
            return pages(path)

        with patch.object(self.api, "pages", side_effect=unavailable_first_review):
            with self.assertRaises(event.EventError):
                self.prepare()
        self.assertEqual([path for path, _ in self.api.posts], [PREFIX + "/statuses/" + "e" * 40])
        self.assertEqual(self.api.posts[0][1]["state"], "pending")

    def test_untrusted_unresolved_signal_cannot_invalidate_approved_or_unreviewed_pulls(self):
        self.add_approved_pull()
        self.api.page_responses[PREFIX + "/pulls/12/reviews"] = []
        self.api.responses[PREFIX + "/actions/runs/7"]["pull_requests"] = None
        with self.assertRaises(event.EventError):
            self.prepare()
        self.assertEqual(self.api.posts, [])

    def test_malformed_or_unavailable_live_reviews_cannot_authorize_invalidation(self):
        for reviews in (None, [dict(review_document(), state="DISMISSED", pull_request_url="other")],
                        [dict(review_document(), state="DISMISSED", submitted_at=None)]):
            self.api.page_responses[PREFIX + "/pulls/12/reviews"] = reviews
            with self.subTest(reviews=reviews), self.assertRaises((ValueError, TypeError)):
                self.prepare()
            self.assertEqual(self.api.posts, [])
        with patch.object(self.api, "pages", side_effect=TimeoutError("unavailable")):
            with self.assertRaises(TimeoutError):
                self.prepare()
        self.assertEqual(self.api.posts, [])

    def test_dismissal_does_not_bypass_source_identity_checks(self):
        for field, value in (("path", "other.yml"), ("event", "push"), ("repository", dict(id=99)),
                             ("status", "in_progress"), ("name", "Other"), ("id", 99)):
            self.api.responses[PREFIX + "/actions/runs/7"] = dict(signal_document(), **{field: value})
            with self.subTest(field=field), self.assertRaises(event.EventError):
                self.prepare()
            self.assertEqual(self.api.posts, [])

    def test_owner_edited_review_wakes_analysis_but_cannot_publish_success(self):
        self.api.responses[PREFIX + "/actions/runs/7"]["actor"]["login"] = OWNER
        self.api.page_responses[PREFIX + "/pulls/12/reviews"] = [dict(review_document(), body="edited away")]
        self.assertEqual(self.prepare(), SNAPSHOT)
        with self.assertRaises(event.EventError):
            event.publish(self.api, SNAPSHOT, result_document(["Makefile"]), "success", [OWNER],
                          suppression_document(), "success")
        self.assertEqual([data["state"] for _, data in self.api.posts], ["pending", "failure"])


class ArtifactTests(unittest.TestCase):
    def test_exact_json_file_is_parsed_without_extracting(self):
        self.assertEqual(event.artifact_pull_number(archive()), 12)

    def test_unsafe_zip_entries_and_json_are_rejected(self):
        bad_archives = [archive(name="../reuse-review-wakeup.json"), archive(mode=0o120777),
                        archive(body=b"x" * 1025), archive(body=b'{"pull_number":true}'),
                        archive(body=b'{"pull_number":12,"pull_number":13}'),
                        archive(body=b'{"pull_number":12,"approved":true}')]
        two = io.BytesIO()
        with zipfile.ZipFile(two, "w") as zipped:
            zipped.writestr("reuse-review-wakeup.json", '{"pull_number":12}')
            zipped.writestr("extra", "bad")
        bad_archives.append(two.getvalue())
        for contents in bad_archives:
            with self.subTest(contents=contents[:30]), self.assertRaises(ValueError):
                event.artifact_pull_number(contents)

    def test_artifact_identity_expiry_duplicate_and_size_are_enforced(self):
        for field, value in (("name", "reuse-review-wakeup-7-1"), ("expired", True),
                             ("size_in_bytes", 65537)):
            api = FakeAPI()
            api.responses[PREFIX + "/actions/runs/7/artifacts?per_page=100&page=1"]["artifacts"][0][field] = value
            signal = signal_document()
            with self.subTest(field=field), self.assertRaises(event.EventError):
                event.signal_artifact(api, REPOSITORY, signal)
        api = FakeAPI()
        artifacts = api.responses[PREFIX + "/actions/runs/7/artifacts?per_page=100&page=1"]["artifacts"]
        artifacts.append(copy.deepcopy(artifacts[0]))
        signal = signal_document()
        with self.assertRaises(event.EventError):
            event.signal_artifact(api, REPOSITORY, signal)

    def test_download_strips_authorization_at_storage_boundary(self):
        api = event.GitHub("test-token")
        requests = []
        location = "https://results.blob.core.windows.net/archive?signature=bounded"

        def open_request(request, timeout):
            requests.append(request)
            self.assertEqual(timeout, 30)
            if len(requests) == 1:
                raise urllib.error.HTTPError(request.full_url, 302, "Found", {"Location": location}, None)
            return io.BytesIO(archive())

        with patch.object(api.opener, "open", side_effect=open_request):
            self.assertEqual(event.artifact_pull_number(api.artifact_archive(REPOSITORY, 99)), 12)
        self.assertEqual(requests[0].get_header("Authorization"), "Bearer test-token")
        self.assertIsNone(requests[1].get_header("Authorization"))
        self.assertEqual(requests[1].full_url, location)

    def test_actual_redirect_handler_preserves_boundary_without_following(self):
        requests = []
        location = "https://results.blob.core.windows.net/archive?signature=bounded"

        class MemoryHTTPS(urllib.request.HTTPSHandler):
            def https_open(self, request):
                requests.append(request)
                storage = request.full_url == location
                headers = {} if storage else {"Location": location}
                contents = archive() if storage else b""
                response = urllib.response.addinfourl(io.BytesIO(contents), headers,
                                                       request.full_url, 200 if storage else 302)
                response.msg = "OK" if storage else "Found"
                return response

        api = event.GitHub("test-token")
        api.opener = urllib.request.build_opener(MemoryHTTPS(), event.NoRedirect())
        self.assertEqual(event.artifact_pull_number(api.artifact_archive(REPOSITORY, 99)), 12)
        self.assertEqual(len(requests), 2)
        self.assertEqual(requests[0].get_header("Authorization"), "Bearer test-token")
        self.assertIsNone(requests[1].get_header("Authorization"))
        # The same handler must reject ordinary API redirects outright.
        with self.assertRaises(event.EventError):
            api.request(PREFIX)
        self.assertEqual(len(requests), 3)

    def test_redirect_rejects_arbitrary_hosts_credentials_schemes_and_ports(self):
        for location in ("http://results.blob.core.windows.net/file", "https://evil.example/file",
                         "https://token@results.blob.core.windows.net/file",
                         "https://results.blob.core.windows.net:444/file", "file:///tmp/archive"):
            with self.subTest(location=location), self.assertRaises(event.EventError):
                event.artifact_location(location)


class PublicationTests(unittest.TestCase):
    @staticmethod
    def publish(api, snapshot, result, outcome, reviewers):
        event.publish(api, snapshot, result, outcome, reviewers, suppression_document(), "success")

    def test_success_requires_exact_analysis_and_current_policy_review(self):
        api = FakeAPI()
        self.publish(api, SNAPSHOT, result_document(["scripts/reuse_event.py"]), "success", [OWNER])
        self.assertEqual(api.posts[-1][1]["state"], "success")

    def test_missing_failed_or_mismatched_analysis_cannot_succeed(self):
        bad = result_document()
        bad["snapshot"]["base"] = "d" * 40
        for outcome, result in (("failure", None), ("cancelled", None), ("skipped", None),
                                ("success", {}), ("success", bad),
                                ("success", dict(result_document(), detector_exit=1))):
            api = FakeAPI()
            with self.subTest(outcome=outcome, result=result), self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, result, outcome, [OWNER])
            self.assertEqual(api.posts[-1][1]["state"], "failure")

    def test_missing_unsuccessful_or_malformed_suppression_receipt_cannot_succeed(self):
        receipts = (None, {}, dict(suppression_document(), baseSHA="d" * 40),
                    dict(suppression_document(), headSHA="d" * 40),
                    dict(suppression_document(), suppressionCount=1),
                    dict(suppression_document(), suppressionCount=False),
                    dict(suppression_document(), artifactId=True),
                    dict(suppression_document(), runId=9007199254740992),
                    dict(suppression_document(), injected="approved"))
        cases = [("success", receipt) for receipt in receipts]
        cases.extend((outcome, suppression_document()) for outcome in ("failure", "cancelled", "skipped"))
        for outcome, receipt in cases:
            api = FakeAPI()
            analysis = result_document()
            with self.subTest(outcome=outcome, receipt=receipt), self.assertRaises(event.EventError):
                event.publish(api, SNAPSHOT, analysis, "success", [OWNER], receipt, outcome)
            self.assertEqual([data["state"] for _, data in api.posts], ["failure"])

    def test_latest_producer_must_keep_exact_attempt_success_and_association(self):
        cases = (("run_attempt", 3), ("status", "in_progress"), ("conclusion", "cancelled"),
                 ("pull_requests", []), ("head_repository", dict(id=999)),
                 ("pull_requests", [dict(pull_document(), base=dict(sha="d" * 40, repo=dict(id=10)))]))
        for key, value in cases:
            api = FakeAPI()
            api.responses[PREFIX + "/actions/runs/41"][key] = value
            analysis = result_document()
            with self.subTest(key=key, value=value), self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, analysis, "success", [OWNER])
            self.assertEqual([data["state"] for _, data in api.posts], ["failure"])

    def test_newer_failed_unassociated_or_stale_base_run_never_falls_back(self):
        for scenario in ("failed", "unassociated", "stale", "queued"):
            api = FakeAPI()
            newer = dict(ci_document(), id=42)
            if scenario == "failed":
                newer["conclusion"] = "failure"
            elif scenario == "unassociated":
                newer["pull_requests"] = []
            elif scenario == "queued":
                newer.update(status="queued", conclusion=None)
            else:
                newer["pull_requests"][0]["base"]["sha"] = "d" * 40
            api.responses[CI_RUNS]["workflow_runs"].insert(0, newer)
            api.responses[CI_RUNS]["total_count"] = 2
            api.responses[PREFIX + "/actions/runs/42"] = newer
            analysis = result_document()
            with self.subTest(scenario=scenario), self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, analysis, "success", [OWNER])
            self.assertEqual([data["state"] for _, data in api.posts], ["failure"])

    def test_artifact_expiry_identity_metadata_and_attempt_window_are_rechecked(self):
        cases = (("expired", True), ("id", 102), ("name", "other"), ("size_in_bytes", 8388609),
                 ("created_at", "2026-09-30T11:59:00Z"), ("created_at", "2026-09-30T12:11:00Z"),
                 ("created_at", "invalid"), ("digest", None),
                 ("workflow_run", dict(id=40, head_sha=HEAD, repository_id=10, head_repository_id=20)))
        for key, value in cases:
            api = FakeAPI()
            api.responses[PREFIX + "/actions/artifacts/101"][key] = value
            analysis = result_document()
            with self.subTest(key=key, value=value), self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, analysis, "success", [OWNER])
            self.assertEqual([data["state"] for _, data in api.posts], ["failure"])

    def test_producer_or_artifact_change_during_status_write_corrects_success(self):
        for scenario in ("rerun", "expired", "metadata"):
            api = FakeAPI()
            request = api.request

            def change_during_write(path, data=None):
                if data is not None and data["state"] == "success":
                    if scenario == "rerun":
                        api.responses[PREFIX + "/actions/runs/41"]["run_attempt"] = 3
                    else:
                        artifact = api.responses[PREFIX + "/actions/artifacts/101"]
                        artifact["expired" if scenario == "expired" else "digest"] = True if scenario == "expired" else "sha256:" + "b" * 64
                return request(path, data)

            with self.subTest(scenario=scenario), patch.object(api, "request", side_effect=change_during_write):
                analysis = result_document()
                with self.assertRaises(event.EventError):
                    self.publish(api, SNAPSHOT, analysis, "success", [OWNER])
            self.assertEqual([data["state"] for _, data in api.posts], ["success", "failure"])

    def test_withdrawal_missing_signoff_and_stale_base_fail(self):
        for scenario in ("withdraw", "absent", "stale"):
            api = FakeAPI()
            if scenario == "absent":
                api.page_responses[PREFIX + "/pulls/12/reviews"] = []
            elif scenario == "withdraw":
                api.page_responses[PREFIX + "/pulls/12/reviews"][0]["body"] = policy.format_signoff(
                    repository=REPOSITORY, pull_request=12, base=BASE, head=HEAD, decision="withdraw")
            else:
                api.responses[PREFIX + "/pulls/12"]["base"]["sha"] = "d" * 40
            result = result_document(["Makefile"])
            with self.subTest(scenario=scenario), self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, result, "success", [OWNER])
            self.assertEqual(api.posts[-1][1]["state"], "failure")

    def test_review_change_between_reads_cannot_publish_success(self):
        api = FakeAPI()
        result = result_document()
        with patch.object(api, "pages", side_effect=[[review_document()], []]):
            with self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, result, "success", [OWNER])
        self.assertEqual(api.posts[-1][1]["state"], "failure")

    def test_final_head_change_cannot_publish_success(self):
        api = FakeAPI()
        result = result_document()
        with patch.object(event, "same_live_pair", side_effect=[None, None, event.EventError("changed")]):
            with self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, result, "success", [OWNER])
        self.assertEqual(api.posts[-1][1]["state"], "failure")

    def test_success_overtaken_by_withdrawal_is_corrected_to_failure(self):
        api = FakeAPI()
        request = api.request

        def withdraw_during_write(path, data=None):
            if data is not None and data["state"] == "success":
                api.page_responses[PREFIX + "/pulls/12/reviews"][0]["body"] = policy.format_signoff(
                    repository=REPOSITORY, pull_request=12, base=BASE, head=HEAD, decision="withdraw")
                request(path, dict(data, state="failure"))
            return request(path, data)

        result = result_document(["Makefile"])
        with patch.object(api, "request", side_effect=withdraw_during_write):
            with self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, result, "success", [OWNER])
        self.assertEqual([data["state"] for _, data in api.posts], ["failure", "success", "failure"])

    def test_post_success_read_timeout_corrects_status_to_failure(self):
        for error in (TimeoutError("read timed out"), http.client.IncompleteRead(b"partial", 100)):
            api = FakeAPI()
            reviews = [review_document()]
            result = result_document()
            with self.subTest(error=error), patch.object(event, "review_evidence", side_effect=[reviews, reviews, error]):
                with self.assertRaises(event.EventError):
                    self.publish(api, SNAPSHOT, result, "success", [OWNER])
            self.assertEqual([data["state"] for _, data in api.posts], ["success", "failure"])

    def test_api_normalizes_truncated_http_reads_to_controlled_failure(self):
        api = event.GitHub("test-token")

        class BrokenResponse(io.BytesIO):
            def read(self, size=-1):
                raise http.client.IncompleteRead(b"partial", 100)

        with patch.object(api.opener, "open", return_value=BrokenResponse()):
            with self.assertRaises(event.EventError):
                api.request(PREFIX)

    def test_all_review_pages_are_loaded_and_truncation_fails(self):
        api = event.GitHub("test-token")
        with patch.object(api, "request", side_effect=[[{}] * 100, [review_document()]]) as request:
            self.assertEqual(len(api.pages(PREFIX + "/pulls/12/reviews")), 101)
            self.assertTrue(request.call_args.args[0].endswith("page=2"))
        with patch.object(api, "request", return_value=[{}] * 100):
            with self.assertRaises(event.EventError):
                api.pages(PREFIX + "/pulls/12/reviews")

    def test_complete_ci_run_pagination_selects_latest_after_all_pages(self):
        api = FakeAPI()
        runs = [dict(ci_document(), id=identifier) for identifier in range(1, 101)]
        api.responses[CI_RUNS] = dict(total_count=101, workflow_runs=runs)
        second = CI_RUNS.replace("page=1&", "page=2&")
        api.responses[second] = dict(total_count=101, workflow_runs=[dict(ci_document(), id=105)])
        api.responses[PREFIX + "/actions/runs/105"] = dict(ci_document(), id=105)
        self.assertEqual(event.latest_ci_run(api, SNAPSHOT)["id"], 105)

    def test_exact_thousand_ci_runs_complete_without_requesting_capped_page(self):
        api = FakeAPI()
        for page in range(1, 11):
            rows = [dict(ci_document(), id=identifier) for identifier in range(100 * (page - 1) + 1, 100 * page + 1)]
            api.responses[CI_RUNS.replace("page=1&", f"page={page}&")] = dict(total_count=1000, workflow_runs=rows)
        api.responses[PREFIX + "/actions/runs/1000"] = dict(ci_document(), id=1000)
        self.assertEqual(event.latest_ci_run(api, SNAPSHOT)["id"], 1000)

    def test_incomplete_capped_duplicate_or_unfiltered_ci_run_pages_fail(self):
        cases = (dict(total_count=1001, workflow_runs=[ci_document()]),
                 dict(total_count=2, workflow_runs=[ci_document()]),
                 dict(total_count=2, workflow_runs=[ci_document(), ci_document()]),
                 dict(total_count=1, workflow_runs=[dict(ci_document(), head_sha="d" * 40)]),
                 dict(total_count=1, workflow_runs=[dict(ci_document(), event="push")]),
                 dict(total_count=0, workflow_runs=[]),
                 dict(total_count=True, workflow_runs=[ci_document()]))
        for response in cases:
            api = FakeAPI()
            api.responses[CI_RUNS] = response
            analysis = result_document()
            with self.subTest(response=response), self.assertRaises(event.EventError):
                self.publish(api, SNAPSHOT, analysis, "success", [OWNER])
            self.assertEqual([data["state"] for _, data in api.posts], ["failure"])

    def test_ci_pagination_count_drift_and_live_run_substitution_fail(self):
        api = FakeAPI()
        api.responses[CI_RUNS] = dict(total_count=101, workflow_runs=[dict(ci_document(), id=i) for i in range(1, 101)])
        api.responses[CI_RUNS.replace("page=1&", "page=2&")] = dict(total_count=102, workflow_runs=[dict(ci_document(), id=105)])
        with self.assertRaises(event.EventError):
            event.latest_ci_run(api, SNAPSHOT)
        api = FakeAPI()
        api.responses[PREFIX + "/actions/runs/41"]["id"] = 40
        with self.assertRaises(event.EventError):
            event.latest_ci_run(api, SNAPSHOT)

    def test_untrusted_api_urls_are_never_opened(self):
        api = event.GitHub("test-token")
        with patch.object(api.opener, "open") as opener:
            for path in ("https://evil.example", "//evil.example", "/repos/a/b/../secret", "/repos/a/b\nX: bad"):
                with self.subTest(path=path), self.assertRaises(event.EventError):
                    api.request(path)
            opener.assert_not_called()


class InputBoundaryTests(unittest.TestCase):
    def test_api_requests_keep_fixed_origin_and_encoded_branch_identity(self):
        api = event.GitHub("test-token")
        paths = (PREFIX, PREFIX + "/pulls/12/reviews?per_page=100&page=2",
                 PREFIX + "/git/ref/heads/release%2Fv1.8.9")
        for path in paths:
            with self.subTest(path=path), patch.object(
                    api.opener, "open", return_value=io.BytesIO(b'{}')) as opener:
                self.assertEqual(api.request(path), {})
                request = opener.call_args.args[0]
                self.assertEqual(request.full_url, "https://api.github.com" + path)
                self.assertEqual(request.get_header("Authorization"), "Bearer test-token")

    def test_api_traversal_and_origin_changes_fail_before_network_access(self):
        api = event.GitHub("test-token")
        paths = (
            "https://api.github.com" + PREFIX,
            "//evil.example" + PREFIX,
            PREFIX + "/pulls/../issues", PREFIX + "/pulls/%2e%2e/issues",
            PREFIX + "/git/ref/heads/release%2F..%2Fmain",
            PREFIX + "/git/ref/heads/release%2F.%2Fmain",
            PREFIX + "/pulls/%252e%252e/issues", PREFIX + "/pulls/%zz",
            PREFIX + "/pulls/%5csecret", PREFIX + "/pulls/%00secret",
            PREFIX + "/pulls\n/12", PREFIX + "/pulls\r/12",
            PREFIX + "/pulls//12", PREFIX + "/pulls/12#other",
        )
        with patch.object(api.opener, "open") as opener:
            for path in paths:
                with self.subTest(path=path), self.assertRaises(ValueError):
                    api.request(path)
            opener.assert_not_called()

    def test_api_query_accepts_only_complete_bounded_pagination(self):
        api = event.GitHub("test-token")
        queries = (
            "page=2", "per_page=100", "per_page=100&page=", "per_page=50&page=2",
            "per_page=100&page=0", "per_page=100&page=-1", "per_page=100&page=1000",
            "per_page=100&page=2&page=3", "per_page=100&per_page=100&page=2",
            "per_page=100&page=2&redirect=", "per_page=100&page=2&redirect=evil",
            "per_page=100&page=2&broken", "per_page=100&page=%2B2",
        )
        with patch.object(api.opener, "open") as opener:
            for query in queries:
                path = PREFIX + "/pulls?" + query
                with self.subTest(query=query), self.assertRaises(ValueError):
                    api.request(path)
            opener.assert_not_called()

    def test_ci_filters_are_exact_and_confined_to_ci_run_endpoint(self):
        self.assertEqual(event.github_api_url(CI_RUNS), "https://api.github.com" + CI_RUNS)
        invalid = (CI_RUNS.replace(HEAD, "main"), CI_RUNS.replace("pull_request", "push"),
                   CI_RUNS + "&head_sha=" + HEAD, CI_RUNS.replace("ci.yml/runs", "other.yml/runs"),
                   CI_RUNS.replace("&event=pull_request", ""))
        for path in invalid:
            with self.subTest(path=path), self.assertRaises(ValueError):
                event.github_api_url(path)

    def test_evidence_uses_fixed_runner_files_before_and_after_creation(self):
        with tempfile.TemporaryDirectory(prefix="reuse-evidence-path-") as directory:
            runner = Path(directory).resolve()
            with patch.dict(os.environ, RUNNER_TEMP=str(runner)):
                for name, filename in (("snapshot", "reuse-snapshot.json"), ("result", "reuse-result.json"),
                                       ("suppression", "reuse-suppression.json")):
                    expected = runner / filename
                    with self.subTest(name=name):
                        self.assertEqual(event.evidence_path(str(expected), name), expected)
                        expected.write_text('{}')
                        self.assertEqual(event.evidence_path(str(expected), name), expected)

    def test_malformed_or_missing_successful_job_outputs_reach_publication_failure(self):
        with tempfile.TemporaryDirectory(prefix="reuse-publication-input-") as directory:
            root = Path(directory).resolve()
            path = root / "reuse-suppression.json"
            with patch.dict(os.environ, RUNNER_TEMP=str(root)):
                for contents in (None, "malformed", '{"runId":41,"runId":42}'):
                    if contents is not None:
                        path.write_text(contents)
                    receipt = event.publication_document(path, "suppression", "success")
                    api = FakeAPI()
                    analysis = result_document()
                    with self.subTest(contents=contents), self.assertRaises(event.EventError):
                        event.publish(api, SNAPSHOT, analysis, "success", [OWNER], receipt, "success")
                    self.assertEqual([data["state"] for _, data in api.posts], ["failure"])

    def test_evidence_outside_scratch_or_wrong_filename_is_rejected(self):
        with tempfile.TemporaryDirectory(prefix="reuse-evidence-path-") as directory:
            root = Path(directory).resolve()
            runner = root / "runner"
            runner.mkdir()
            paths = (root / "reuse-snapshot.json", runner / "other.json", runner / "reuse-result.json")
            with patch.dict(os.environ, RUNNER_TEMP=str(runner)):
                for path in paths:
                    with self.subTest(path=path), self.assertRaises(event.EventError):
                        event.evidence_path(path, "snapshot")
                expected = runner / "reuse-snapshot.json"
                with self.assertRaises(event.EventError):
                    event.evidence_path(expected, "unsupported")

    def test_evidence_symlink_cannot_redirect_fixed_file_reads_or_writes(self):
        with tempfile.TemporaryDirectory(prefix="reuse-evidence-path-") as directory:
            root = Path(directory).resolve()
            runner = root / "runner"
            runner.mkdir()
            external = root / "external.json"
            expected = runner / "reuse-snapshot.json"
            expected.symlink_to(external)
            with patch.dict(os.environ, RUNNER_TEMP=str(runner)):
                with self.assertRaises(event.EventError):
                    event.evidence_path(expected, "snapshot")
                self.assertFalse(external.exists())
                external.write_text('{"untrusted":true}')
                with self.assertRaises(event.EventError):
                    event.evidence_path(expected, "snapshot")
                self.assertEqual(external.read_text(), '{"untrusted":true}')


class CandidateTests(unittest.TestCase):
    def test_candidate_merge_uses_exact_ref_without_checkout_or_hooks(self):
        with tempfile.TemporaryDirectory(prefix="reuse-event-test-") as directory:
            root = Path(directory)

            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root,
                                               env=event.git_environment(), stderr=subprocess.PIPE).decode().strip()

            git("init", "-q")
            git("config", "user.name", "Event Test")
            git("config", "user.email", "event@example.invalid")
            (root / "file").write_text("base\n")
            git("add", "file")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "file").write_text("candidate\n")
            (root / "Makefile").write_text("all:\n\ttouch EXECUTED\n")
            git("add", "file", "Makefile")
            git("commit", "-qm", "head")
            head = git("rev-parse", "HEAD")
            git("reset", "--hard", base)
            hook = root / ".git/hooks/post-commit"
            hook.write_text("#!/bin/sh\ntouch EXECUTED\n")
            hook.chmod(0o700)
            snapshot = dict(SNAPSHOT, base=base, head=head)
            original = event.git

            def offline_git(directory, *args, **kwargs):
                if args[0] == "fetch":
                    return original(directory, "update-ref", "refs/reuse/event-head", head)
                return original(directory, *args, **kwargs)

            with patch.object(event, "git", side_effect=offline_git):
                candidate = event.candidate_commit(root, snapshot, "test-token")
            self.assertEqual(git("show", candidate + ":file"), "candidate")
            self.assertEqual(git("rev-parse", "HEAD"), base)
            self.assertFalse((root / "Makefile").exists())
            self.assertFalse((root / "EXECUTED").exists())
            self.assertEqual(git("rev-parse", candidate + "^1"), base)
            self.assertEqual(git("rev-parse", candidate + "^2"), head)
            mismatched_snapshot = dict(snapshot, head="c" * 40)
            with patch.object(event, "git", side_effect=offline_git), self.assertRaises(event.EventError):
                event.candidate_commit(root, mismatched_snapshot, "test-token")
            git("config", "merge.custom.driver", "touch EXECUTED")
            with self.assertRaises(event.EventError):
                event.candidate_commit(root, snapshot, "test-token")

    def test_detector_environment_removes_credentials_and_workflow_output_files(self):
        with patch.dict(os.environ, GH_TOKEN="secret", GITHUB_TOKEN="secret", GITHUB_OUTPUT="output",
                        GITHUB_ENV="environment", GIT_CONFIG_COUNT="99", ACTIONS_RUNTIME_TOKEN="secret"):
            clean = event.git_environment()
        for name in ("GH_TOKEN", "GITHUB_TOKEN", "GITHUB_OUTPUT", "GITHUB_ENV", "GIT_CONFIG_COUNT", "ACTIONS_RUNTIME_TOKEN"):
            self.assertNotIn(name, clean)

    def test_snapshot_and_result_schema_reject_injected_fields_and_invalid_ids(self):
        for snapshot in (dict(SNAPSHOT, head="main"), dict(SNAPSHOT, pull_number=True),
                         dict(SNAPSHOT, command="exec"), dict(SNAPSHOT, repository="a/..")):
            with self.subTest(snapshot=snapshot), self.assertRaises(ValueError):
                event.validate_snapshot(snapshot)
        unrelated_policy_result = result_document(["README.md"])
        with self.assertRaises(ValueError):
            event.validate_result(unrelated_policy_result, SNAPSHOT)


if __name__ == "__main__":
    unittest.main()
