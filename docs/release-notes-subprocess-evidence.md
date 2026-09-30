# Release-note subprocess boundary

Issue #1648 and Sonar finding `AaCK61tceMZvS3_EWBJE` (`pythonsecurity:S8705`)
identified the variadic Git helper used by the release-note scripts. The helper
has been replaced with three fixed-command operations. Revisions never become
command-line arguments:

| Operation | Fixed Git command | Standard input |
| --- | --- | --- |
| Read a release file | `cat-file --batch` | One validated `revision:path` request |
| List extension commits | `log --format=%H%x00%s --stdin -- extensions/vscode-lopper` | One validated stable-tag-to-HEAD range |
| Inspect commit paths | `diff-tree --no-commit-id --name-only -r --stdin` | One validated full commit ID |

## Production caller audit

The release-note scripts call these operations through the following paths:

| Caller | Operation | Input and origin |
| --- | --- | --- |
| `release_build_notes.generate` | `git_file` | Validated previous stable tag and fixed `go.mod` path |
| `old_lockfile` | `git_file` | Independently validated previous stable tag and fixed extension lockfile path |
| `old_package` | `git_file` | Independently validated previous stable tag and fixed extension package path |
| `visible_extension_commits` | `git_extension_log` | Previous stable tag supplied by `source_notes` from the VS Code generator |
| `visible_extension_commits` | `git_commit_paths` | Full commit ID from Git's `%H` log output |
| `configured_marketplace_icon_path` | `git_file` | Commit ID from the same log and fixed extension package path |
| `user_visible_manifest_change` | `git_file` | Current commit ID and its parent from the same log, each with the fixed extension package path |

Both CLI entry points still reject invalid stable tags before file reads or
subprocess execution. Each Git operation also validates its own input. Tags use
three one-to-nine-digit components after `v`; commit IDs are exactly 40 or 64
hexadecimal characters, with an optional parent operator only for file reads.
File requests permit only `go.mod` and the fixed extension package and lockfile
paths. Newlines, option-like values, revision expressions, and arbitrary paths
are rejected before Git starts, so inputs cannot inject additional protocol
records or options.

`cat-file` responses must be blobs with the exact advertised byte count and
trailing record delimiter. Missing objects retain the previous
`CalledProcessError` behavior. All commands use argument lists without a shell;
the repository path remains the single argument following `-C`.

The release workflow selects reachable stable tags and invokes separately
checked-out trusted main tooling. These checks constrain the script inputs;
they do not sandbox a malicious Git executable or repository configuration.

## Validation

```sh
go test ./scripts -run TestReleaseNotesRejectInvalidTagsBeforeSubprocess -count=1
python3 -B scripts/vscode_release_notes_test.py
python3 -B scripts/release_build_notes_test.py
```

The Go suite runs the Python security probes, including both actual `__main__`
paths, invalid tags, protocol injection, allowed file paths, malformed responses,
and fixed-command/stdin assertions. A positive control confirms the subprocess
interceptor observes a real invocation boundary. Existing temporary-repository
tests exercise successful release generation, commit inspection, parent
manifests, and missing historical files using Git itself.

Each Python suite also calls its real argument parser and CLI handler with
malformed tags while intercepting `subprocess.Popen`, asserting that no process
starts. Direct rejection probes for `old_lockfile` and `old_package` verify that
these exported readers enforce the tag boundary independently of the CLI.

This implementation supersedes PR #1727's evidence-only disposition proposal.
It changes the subprocess boundary in code without scanner suppressions or
quality-gate changes.
