#!/usr/bin/env python3
"""Exercise policy path coverage and exact-revision agent signoff evidence."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import reuse_policy_review as policy


class SignoffTests(unittest.TestCase):
    def setUp(self):
        self.context = dict(repository="ben-ranford/lopper", pull_request=1612,
                            base="a" * 40, head="b" * 40,
                            allowed_reviewers=("ben-ranford",), reviews_complete=True)

    def body(self, **changes):
        document = {key: self.context[key] for key in ("repository", "pull_request", "base", "head")}
        document.update(version=1, decision="approve")
        document.update(changes)
        return "agent-reviewed\n" + json.dumps(document)

    def review(self, identifier=1, **changes):
        review = dict(id=identifier, user={"login": "ben-ranford", "type": "User"},
                      state="COMMENTED", body=self.body(), commit_id=self.context["head"],
                      pull_request_url="https://api.github.com/repos/ben-ranford/lopper/pulls/1612",
                      submitted_at=f"2026-10-01T12:00:{identifier:02d}Z")
        review.update(changes)
        return review

    def evaluate(self, reviews, **changes):
        return policy.evaluate_signoff(reviews, **dict(self.context, **changes))

    def test_exact_agent_review_passes_with_explicit_attribution(self):
        result = self.evaluate([self.review()])
        self.assertEqual(result["label"], "agent-reviewed")
        self.assertEqual(result["review_id"], 1)
        self.assertEqual(result["reviewer"], "ben-ranford")
        for name in ("repository", "pull_request", "base", "head"):
            self.assertEqual(result[name], self.context[name])
        rendered = policy.format_signoff(**{
            name: result[name] for name in ("repository", "pull_request", "base", "head", "decision")})
        self.assertEqual(policy.parse_signoff(rendered), json.loads(self.body().split("\n", 1)[1]))

    def test_body_and_native_association_cannot_appoint_reviewer(self):
        forged = self.review(user={"login": "contributor", "type": "User"}, author_association="OWNER")
        with self.assertRaises(policy.PolicyReviewError):
            self.evaluate([forged])
        self_appointed = self.review(body=self.body(allowed_reviewers=["contributor"]))
        with self.assertRaises(policy.PolicyReviewError):
            self.evaluate([self_appointed])
        self.assertEqual(self.evaluate([
            self.review(user={"login": "BEN-RANFORD", "type": "User"})])["review_id"], 1)

    def test_latest_withdrawal_blocks_regardless_of_list_order(self):
        approval = self.review()
        withdrawal = self.review(2, body=self.body(decision="withdraw"))
        for reviews in ([approval, withdrawal], [withdrawal, approval]):
            with self.subTest(reviews=reviews):
                with self.assertRaisesRegex(policy.PolicyReviewError, "withdraws"):
                    self.evaluate(reviews)

    def test_fresh_explicit_approval_can_follow_withdrawal(self):
        self.assertEqual(self.evaluate([
            self.review(body=self.body(decision="withdraw")), self.review(2)])["review_id"], 2)

    def test_stale_latest_decision_never_falls_back_to_old_approval(self):
        for changes in ({"base": "c" * 40}, {"head": "c" * 40},
                        {"repository": "another/repository"}, {"pull_request": 42}):
            with self.subTest(changes=changes):
                reviews = [self.review(), self.review(2, body=self.body(**changes))]
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews)
        stale_commit_review = self.review(commit_id="c" * 40)
        with self.assertRaisesRegex(policy.PolicyReviewError, "current head"):
            self.evaluate([stale_commit_review])

    def test_edited_away_or_malformed_latest_body_blocks_earlier_approval(self):
        for body in ("", "ordinary review after removing signoff", "agent-reviewed\n{",
                     self.body(decision="withdraw")):
            with self.subTest(body=body):
                reviews = [self.review(), self.review(2, body=body)]
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews)

    def test_dismissal_and_native_approval_cannot_grant_agent_signoff(self):
        for state in ("DISMISSED", "APPROVED", "CHANGES_REQUESTED", "UNKNOWN"):
            with self.subTest(state=state):
                reviews = [self.review(), self.review(2, state=state)]
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews)

    def test_pending_review_neither_grants_nor_removes_submitted_decision(self):
        pending = self.review(2, state="PENDING", submitted_at=None, body=self.body(decision="withdraw"))
        with self.assertRaises(policy.PolicyReviewError):
            self.evaluate([pending])
        self.assertEqual(self.evaluate([self.review(), pending])["review_id"], 1)

    def test_untrusted_later_reviews_cannot_withdraw_authorized_approval(self):
        outsider = self.review(2, user={"login": "other-user"}, body=self.body(decision="withdraw"))
        self.assertEqual(self.evaluate([self.review(), outsider])["review_id"], 1)

    def test_missing_or_incomplete_live_evidence_cannot_pass(self):
        with self.assertRaises(policy.PolicyReviewError):
            self.evaluate([])
        for value in (False, None, 1, "true"):
            with self.subTest(value=value):
                reviews = [self.review()]
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews, reviews_complete=value)
        wrapped_reviews = {"reviews": [self.review()]}
        with self.assertRaises(policy.PolicyReviewError):
            self.evaluate(wrapped_reviews)

    def test_duplicate_records_and_ambiguous_latest_times_fail_closed(self):
        for reviews in ([self.review(), self.review()], [self.review(), self.review(2, submitted_at="2026-10-01T12:00:01Z")]):
            with self.subTest(reviews=reviews):
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews)

    def test_live_metadata_must_belong_to_expected_pr_and_be_well_formed(self):
        for changes in ({"id": True}, {"user": None}, {"user": {}},
                        {"pull_request_url": None}, {"pull_request_url": "https://api.github.com/repos/other/repo/pulls/1612"},
                        {"submitted_at": None}, {"submitted_at": "2026-02-31T12:00:00Z"},
                        {"submitted_at": "2026-10-01T12:00:00+00:00"}):
            with self.subTest(changes=changes):
                reviews = [self.review(**changes)]
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews)

    def test_signoff_schema_and_sha_validation_are_strict(self):
        malformed = [
            self.body(version=True), self.body(version=2), self.body(pull_request=True),
            self.body(base="main"), self.body(head="B" * 40), self.body(base="a" * 39),
            self.body(repository="lopper"), self.body(decision="APPROVE"), self.body(extra="ignored"),
            "agent-reviewed\n[]", "agent-reviewed\nnull", self.body() + "{}",
            self.body().replace('"version": 1', '"version": 1, "version": 1'),
            "human-reviewed\n" + self.body().split("\n", 1)[1],
        ]
        for body in malformed:
            with self.subTest(body=body):
                reviews = [self.review(body=body)]
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews)

    def test_trusted_context_cannot_be_missing_or_malformed(self):
        for changes in ({"repository": "../repo"}, {"pull_request": True}, {"base": "main"},
                        {"head": "b" * 39}, {"allowed_reviewers": []},
                        {"allowed_reviewers": "ben-ranford"}, {"allowed_reviewers": [None]}):
            with self.subTest(changes=changes):
                reviews = [self.review()]
                with self.assertRaises(policy.PolicyReviewError):
                    self.evaluate(reviews, **changes)


class PolicyPathTests(unittest.TestCase):
    def test_policy_surface_includes_metadata_dependencies_and_all_owners(self):
        paths = (
            "Makefile", "go.mod", "go.sum", "go.work", "go.work.sum",
            "CODEOWNERS", "docs/CODEOWNERS", ".github/CODEOWNERS",
            ".github/duplication-baseline.json", ".github/workflows/reuse.yml",
            ".github/actions/check/action.yml", "scripts/github-action/action.yaml",
            "scripts/check_reuse.py", "scripts/check_reuse_test.py",
            "scripts/reuse_policy_review.py", "scripts/reuse_policy_review_test.py",
            "scripts/reuse_event.py", "scripts/reuse_event_test.py",
            "scripts/check_duplication.py", "scripts/duplication_policy.py",
            "scripts/duplication_index.go", "internal/reusecheck/contracts.go",
            "internal/reusecheck/legacy_catalog.go", "tools/reusecheck/main.go",
            ".gitattributes", "internal/.gitattributes", "a/b/.gitattributes",
        )
        for path in paths:
            with self.subTest(path=path):
                self.assertTrue(policy.is_policy_path(path))

    def test_suppression_execution_and_producer_paths_require_review(self):
        for path in ("scripts/reuse_suppression.js", "scripts/suppression_provenance.js",
                     "scripts/inline_suppression_tracker.js", "scripts/check-inline-suppressions.sh"):
            with self.subTest(path=path):
                self.assertTrue(policy.is_policy_path(path))

    def test_queue_callers_and_all_admission_dependencies_require_review(self):
        for path in ("scripts/queue_me_controller.js", "scripts/queue_me_reuse.js",
                     "scripts/queue_me_reuse.py", "scripts/queue_me_ci.js",
                     "scripts/queue_me_ci_intent.js", "scripts/queue_me_public_api.js",
                     "scripts/queue_me_reviews.js", "scripts/queue_me_sonar.js",
                     "scripts/queue_me_suppressions.js"):
            with self.subTest(path=path):
                self.assertTrue(policy.is_policy_path(path))

    def test_unrelated_paths_do_not_create_a_global_review_rule(self):
        for path in ("README.md", "internal/app/main.go", "docs/guide.md",
                     "go.module", "Makefile.example", "docs/CODEOWNERS.md",
                     "internal/reusechecker/check.go", "notes/.gitattributes.md"):
            with self.subTest(path=path):
                self.assertFalse(policy.is_policy_path(path))

    def test_additional_dependencies_come_from_trusted_caller(self):
        paths = ("scripts/runtime/support.py", "internal/shared/parse.go", "unrelated.go")
        self.assertEqual(policy.changed_policy_paths(paths), ())
        self.assertEqual(policy.changed_policy_paths(
            paths, ("scripts/runtime/support.py", "internal/shared")), paths[:2])
        self.assertFalse(policy.is_policy_path("internal/shared-extra/parse.go", ("internal/shared",)))

    def test_noncanonical_paths_fail_instead_of_skipping_review(self):
        for path in ("", "/Makefile", "../Makefile", "docs/../CODEOWNERS",
                     "./Makefile", "docs//CODEOWNERS", "docs\\CODEOWNERS",
                     "docs/CODEOWNERS\n", None):
            with self.subTest(path=path):
                with self.assertRaises(policy.PolicyReviewError):
                    policy.is_policy_path(path)

    def test_git_path_parser_requires_complete_unambiguous_records(self):
        self.assertEqual(policy.parse_changed_paths(b""), ())
        self.assertEqual(policy.parse_changed_paths(b"Makefile\0docs/CODEOWNERS\0"),
                         ("Makefile", "docs/CODEOWNERS"))
        for output in ("Makefile\0", b"Makefile", b"\0", b"Makefile\0Makefile\0", b"\xff\0"):
            with self.subTest(output=output):
                with self.assertRaises(policy.PolicyReviewError):
                    policy.parse_changed_paths(output)

    def test_no_renames_diff_preserves_deleted_and_renamed_policy_paths(self):
        with tempfile.TemporaryDirectory(prefix="reuse-policy-paths-") as directory:
            root = Path(directory)
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith("GIT_")}
            environment.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
                               GIT_CONFIG_NOSYSTEM="1")

            def git(*arguments):
                return subprocess.check_output(
                    ["git", "-c", "core.hooksPath=/dev/null", *arguments],
                    cwd=root, env=environment, stderr=subprocess.PIPE)

            git("init", "-q")
            git("config", "user.name", "Policy Test")
            git("config", "user.email", "policy-test@example.invalid")
            (root / "docs").mkdir()
            (root / "docs/CODEOWNERS").write_text("* @owner\n")
            (root / ".gitattributes").write_text("*.go text\n")
            git("add", "--all")
            git("commit", "-qm", "protected base")
            base = git("rev-parse", "HEAD").decode().strip()
            (root / "docs/CODEOWNERS").rename(root / "docs/old-owners.txt")
            (root / ".gitattributes").unlink()
            git("add", "--all")
            git("commit", "-qm", "policy rename and removal")
            head = git("rev-parse", "HEAD").decode().strip()
            paths = policy.parse_changed_paths(git(
                "diff", "--no-renames", "--name-only", "-z", base, head, "--"))
            self.assertEqual(set(paths), {".gitattributes", "docs/CODEOWNERS", "docs/old-owners.txt"})
            self.assertEqual(set(policy.changed_policy_paths(paths)),
                             {".gitattributes", "docs/CODEOWNERS"})


if __name__ == "__main__":
    unittest.main()
