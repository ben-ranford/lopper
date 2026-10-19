"""Reuse controller wakeups must settle on their own instead of stranding open PRs.

Usage: python3 -B self_healing.py <scripts directory>
"""

import copy
import sys
import unittest

sys.path.insert(0, sys.argv.pop(1))
import reuse_event as event  # noqa: E402

REPOSITORY = "ben-ranford/lopper"
PREFIX = "/repos/" + REPOSITORY
MAIN, OLD_MAIN = "a" * 40, "d" * 40
SUCCESS = "Reuse, suppression and policy review passed; base=" + MAIN + " policy=" + MAIN


def pull(number, head, base=MAIN):
    return dict(number=number, state="open", head=dict(sha=head, ref=f"feature/{number}", repo=dict(id=10)),
                base=dict(sha=base, ref="main", repo=dict(id=10, full_name=REPOSITORY)))


class API:
    """Live repository with two open PRs, both certified, and a CI run of a since-closed PR."""

    def __init__(self, pulls):
        self.pulls = pulls
        self.posts = []
        self.responses = {
            PREFIX: dict(id=10, full_name=REPOSITORY, default_branch="main", owner=dict(login="ben-ranford")),
            PREFIX + "/git/ref/heads/main": dict(object=dict(sha=MAIN)),
            PREFIX + "/actions/workflows/ci.yml": dict(id=6, path=event.CI_WORKFLOW, name="ci"),
            PREFIX + "/actions/runs/41": dict(
                id=41, workflow_id=6, run_attempt=1, path=event.CI_WORKFLOW, name="ci", event="pull_request",
                status="completed", conclusion="success", head_sha="9" * 40, repository=dict(id=10),
                head_repository=dict(id=10), pull_requests=[]),
        }
        for row in pulls:
            self.responses[f"{PREFIX}/pulls/{row['number']}"] = row

    def request(self, path, data=None):
        if data is not None:
            self.posts.append((path.rsplit("/", 1)[1], data["state"], data["description"]))
            return {}
        if "/status?per_page=100&page=" in path:
            return dict(total_count=1, statuses=[dict(context="reuse-check", state="success", description=SUCCESS)])
        return copy.deepcopy(self.responses[path])

    def pages(self, path):
        assert path == PREFIX + "/pulls", path
        return copy.deepcopy(self.pulls)

    def graphql(self, query, variables):
        nodes = [dict(number=row["number"], commits=dict(nodes=[dict(commit=dict(
            oid=row["head"]["sha"], status=dict(context=dict(state="SUCCESS", description=SUCCESS))))]))
            for row in self.pulls]
        return dict(data=dict(repository=dict(pullRequests=dict(
            pageInfo=dict(hasNextPage=False, endCursor=None), nodes=nodes))))


def prepare(api, payload, name):
    return event.prepare(api, dict(payload, repository=dict(id=10)), name, REPOSITORY, 10,
                         ".github/workflows/reuse-review-signal.yml", "ben-ranford")


class SelfHealingTests(unittest.TestCase):
    def test_ci_run_of_a_closed_pr_leaves_other_open_prs_untouched(self):
        api = API([pull(12, "b" * 40), pull(13, "c" * 40)])
        with self.assertRaises(event.EventError) as raised:
            prepare(api, dict(action="completed", workflow_run=dict(id=41)), "workflow_run")
        self.assertEqual(type(raised.exception).__name__, "Settled")
        self.assertEqual(api.posts, [])

    def test_behind_pr_waits_for_a_branch_update_instead_of_failing(self):
        api = API([pull(12, "b" * 40, base=OLD_MAIN)])
        with self.assertRaises(event.EventError) as raised:
            prepare(api, dict(action="synchronize", number=12, pull_request=pull(12, "b" * 40, base=OLD_MAIN)),
                    "pull_request_target")
        self.assertEqual(type(raised.exception).__name__, "Settled")
        self.assertEqual(api.posts, [("b" * 40, "pending",
                                      "Base moved; update the branch so CI runs against the current target")])

    def test_cancelled_concurrency_twin_does_not_hide_its_successful_run(self):
        head = "b" * 40
        api = API([pull(12, head)])
        twins = [dict(api.responses[PREFIX + "/actions/runs/41"], id=41, head_sha=head, created_at="2026-10-19T19:08:37Z"),
                 dict(api.responses[PREFIX + "/actions/runs/41"], id=42, head_sha=head, conclusion="cancelled",
                      created_at="2026-10-19T19:08:37Z")]
        api.responses[PREFIX + "/actions/runs/41"] = twins[0]
        api.responses[PREFIX + "/actions/runs/42"] = twins[1]
        api.responses[f"{PREFIX}/actions/workflows/ci.yml/runs?per_page=100&page=1&head_sha={head}&event=pull_request"] = dict(
            total_count=2, workflow_runs=twins)
        snapshot = dict(repository=REPOSITORY, repository_id=10, head=head)
        self.assertEqual(event.latest_ci_run(api, snapshot)["id"], 41)


if __name__ == "__main__":
    unittest.main()
