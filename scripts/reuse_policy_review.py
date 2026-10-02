"""Validate explicit agent review from trusted metadata; never fetch or publish it.

The caller supplies live, fully paginated reviews and repository policy from a
trusted source. Candidate files and comment bodies cannot appoint reviewers.
"""

import datetime
import json
import re


class PolicyReviewError(ValueError):
    """The supplied evidence does not establish an authorized policy review."""


POLICY_FILES = frozenset((
    "Makefile", "go.mod", "go.sum", "go.work", "go.work.sum", "CODEOWNERS",
    "docs/CODEOWNERS", ".github/CODEOWNERS",
))
POLICY_DIRECTORIES = (".github/", "internal/reusecheck/", "tools/reusecheck/")
POLICY_SCRIPT_PREFIXES = (
    "scripts/check_reuse", "scripts/reuse_policy_review", "scripts/reuse_event", "scripts/check_duplication",
    "scripts/duplication_", "scripts/reuse_suppression", "scripts/suppression_provenance",
    "scripts/inline_suppression_tracker", "scripts/check-inline-suppressions.sh",
)
SIGNOFF_HEADER = "agent-reviewed\n"
SIGNOFF_KEYS = frozenset(("version", "repository", "pull_request", "base", "head", "decision"))


def immutable_sha(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{40}", value):
        raise PolicyReviewError("Signoff revisions must be full immutable commit SHAs")
    return value


def repository_name(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9-]+/[A-Za-z0-9_.-]+", value):
        raise PolicyReviewError("Repository must be an explicit owner/name")
    return value


def positive_integer(value, label):
    if type(value) is not int or value < 1:
        raise PolicyReviewError(label + " must be a positive integer")
    return value


def validate_signoff(document):
    if not isinstance(document, dict) or set(document) != SIGNOFF_KEYS:
        raise PolicyReviewError("Signoff schema must contain exactly the documented fields")
    if type(document["version"]) is not int or document["version"] != 1:
        raise PolicyReviewError("Unsupported signoff version")
    repository_name(document["repository"])
    positive_integer(document["pull_request"], "Pull request")
    immutable_sha(document["base"])
    immutable_sha(document["head"])
    if document["decision"] not in ("approve", "withdraw"):
        raise PolicyReviewError("Decision must be approve or withdraw")
    return document


def unique_object(pairs):
    document = {}
    for key, value in pairs:
        if key in document:
            raise PolicyReviewError("Duplicate signoff field: " + key)
        document[key] = value
    return document


def parse_signoff(body):
    if not isinstance(body, str) or not body.startswith(SIGNOFF_HEADER):
        raise PolicyReviewError("Latest reviewer decision must be explicitly agent-reviewed")
    try:
        document = json.loads(body[len(SIGNOFF_HEADER):], object_pairs_hook=unique_object)
    except (ValueError, RecursionError) as error:
        raise PolicyReviewError("Malformed agent-reviewed signoff JSON") from error
    return validate_signoff(document)


def format_signoff(*, repository, pull_request, base, head, decision):
    """Render a deliberate decision; this function never submits a review."""
    document = validate_signoff({"version": 1, "repository": repository,
                                 "pull_request": pull_request, "base": base, "head": head,
                                 "decision": decision})
    return SIGNOFF_HEADER + json.dumps(document, sort_keys=True, indent=2)


def review_time(value):
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value):
        raise PolicyReviewError("Submitted review timestamp is missing or invalid")
    try:
        return datetime.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError as error:
        raise PolicyReviewError("Submitted review timestamp is invalid") from error


def trusted_reviewers(allowed_reviewers):
    if not isinstance(allowed_reviewers, (list, tuple, set, frozenset)) or not allowed_reviewers:
        raise PolicyReviewError("Trusted caller must supply allowed reviewer identities")
    for login in allowed_reviewers:
        if not isinstance(login, str) or not re.fullmatch(r"[A-Za-z0-9-]+", login):
            raise PolicyReviewError("Invalid trusted reviewer identity")
    return {login.casefold() for login in allowed_reviewers}


def review_identity(review):
    if not isinstance(review, dict) or not isinstance(review.get("user"), dict):
        raise PolicyReviewError("Malformed live review metadata")
    identifier = positive_integer(review.get("id"), "Review ID")
    login = review["user"].get("login")
    if not isinstance(login, str):
        raise PolicyReviewError("Live review author is missing")
    return identifier, login.casefold()


def authorized_submission(review, login, reviewers, expected_url):
    if login not in reviewers:
        return None
    if review.get("state") == "PENDING" and not review.get("submitted_at"):
        return None
    review_url = review.get("pull_request_url")
    if not isinstance(review_url, str) or review_url.casefold() != expected_url.casefold():
        raise PolicyReviewError("Live reviewer decision belongs to a different pull request")
    return review_time(review.get("submitted_at")), review


def latest_authorized_review(live_reviews, reviewers, expected_url):
    if not isinstance(live_reviews, (list, tuple)):
        raise PolicyReviewError("Live reviews must be a complete sequence")
    decisions = []
    identifiers = set()
    for review in live_reviews:
        identifier, login = review_identity(review)
        if identifier in identifiers:
            raise PolicyReviewError("Duplicate live review ID")
        identifiers.add(identifier)
        submission = authorized_submission(review, login, reviewers, expected_url)
        if submission is not None:
            decisions.append(submission)
    if not decisions:
        raise PolicyReviewError("No submitted agent review from an authorized identity")
    latest_time = max(timestamp for timestamp, _ in decisions)
    latest = [review for timestamp, review in decisions if timestamp == latest_time]
    if len(latest) != 1:
        raise PolicyReviewError("Latest authorized review order is ambiguous")
    return latest[0]


def evaluate_signoff(live_reviews, *, repository, pull_request, base, head,
                     allowed_reviewers, reviews_complete=False):
    """Require the latest authorized submitted review to approve this exact pair.

    Fetch every page of live PR reviews using trusted repository metadata, then
    set reviews_complete=True. Submitted reviews cannot be deleted through the
    GitHub API. Their bodies can be edited, so consider *all* submitted reviews
    from authorized identities: an edited-away marker must not resurrect an
    older approval. No issue comments or native APPROVED states grant approval.
    API state, author, timestamp, commit_id and PR URL must be trusted live data.
    """
    if reviews_complete is not True:
        raise PolicyReviewError("Complete live review retrieval is required")
    repository_name(repository)
    positive_integer(pull_request, "Pull request")
    immutable_sha(base)
    immutable_sha(head)
    reviewers = trusted_reviewers(allowed_reviewers)
    expected_url = f"https://api.github.com/repos/{repository}/pulls/{pull_request}"
    review = latest_authorized_review(live_reviews, reviewers, expected_url)
    if review.get("state") != "COMMENTED":
        raise PolicyReviewError("Latest authorized review must remain a submitted COMMENT review")
    if review.get("commit_id") != head:
        raise PolicyReviewError("Latest review commit does not match the current head")
    document = parse_signoff(review.get("body"))
    if (document["repository"].casefold() != repository.casefold()
            or document["pull_request"] != pull_request
            or document["base"] != base or document["head"] != head):
        raise PolicyReviewError("Latest agent review does not match this repository, PR, base and head")
    if document["decision"] != "approve":
        raise PolicyReviewError("Latest agent review withdraws approval")
    return {"label": "agent-reviewed", "review_id": review["id"],
            "reviewer": review["user"]["login"], **document}


def validate_path(path):
    if (not isinstance(path, str) or not path or path.startswith("/")
            or "\\" in path or any(ord(character) < 32 for character in path)
            or any(part in ("", ".", "..") for part in path.split("/"))):
        raise PolicyReviewError("Changed paths must be canonical relative repository paths")
    return path


def parse_changed_paths(output):
    """Read `git diff --no-renames --name-only -z BASE HEAD --` output.

    Disabling rename detection makes both old and new paths visible, including
    policy removals. The trusted caller owns the command and immutable refs.
    """
    if not isinstance(output, bytes) or output and not output.endswith(b"\0"):
        raise PolicyReviewError("Changed-path input must be complete NUL-delimited bytes")
    try:
        paths = [validate_path(path.decode("utf-8")) for path in output.split(b"\0")[:-1]]
    except UnicodeDecodeError as error:
        raise PolicyReviewError("Cannot validate a non-UTF-8 policy path") from error
    if len(paths) != len(set(paths)):
        raise PolicyReviewError("Duplicate changed paths")
    return tuple(paths)


def is_policy_path(path, dependency_paths=()):
    """Include extra checker dependencies only from protected caller policy."""
    path = validate_path(path)
    dependencies = tuple(validate_path(dependency) for dependency in dependency_paths)
    return (path in POLICY_FILES or path.startswith(POLICY_DIRECTORIES)
            or path.startswith(POLICY_SCRIPT_PREFIXES)
            or path.rsplit("/", 1)[-1] in (".gitattributes", "action.yml", "action.yaml")
            or any(path == dependency or path.startswith(dependency + "/")
                   for dependency in dependencies))


def changed_policy_paths(paths, dependency_paths=()):
    dependencies = tuple(dependency_paths)
    return tuple(path for path in paths if is_policy_path(path, dependencies))
