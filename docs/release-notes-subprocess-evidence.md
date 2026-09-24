# Release-note subprocess boundary evidence

Issue #1648 tracks SonarCloud finding `AaCK61tceMZvS3_EWBJE`
(`pythonsecurity:S8705`) at `scripts/vscode_release_notes.py`'s `git` helper.
The reported path starts at argument parsing in `release_build_notes.main`,
passes `previous_tag` through `generate`, and ends at `subprocess.check_output`.

## Reported path and callers

Both release-note CLI entry points call a `generate` function that rejects a tag
unless `STABLE_TAG.fullmatch` succeeds, before any file read or Git invocation.
The accepted grammar is `v` followed by three dot-separated numeric components,
each one to nine digits. Leading options, spaces, newlines, shell metacharacters,
revision operators, and path separators cannot pass. An accepted value cannot
become a Git option or add a new argv element.

The shared Git helper uses an argument list with the default `shell=False`.
This alone would not prevent option injection; validation before the call is
what constrains the reported tag input. The repository path occupies the single
argument following the fixed `-C` option.

All production helper callers were reviewed:

| Caller | Variable arguments and boundary |
| --- | --- |
| `release_build_notes.generate` | Validated stable tag plus the fixed `:go.mod` suffix, passed to `git show`. |
| `old_lockfile`, `old_package` | Each independently validates the tag, then appends a fixed repository path for `git show`. |
| `visible_extension_commits` | Called by `source_notes` inside validated VS Code `generate`; the tag becomes one `tag..HEAD` revision argument. The extension path follows `--`. |
| `visible_extension_commits`'s `diff-tree` | Commit IDs come from Git's `%H` output, not commit subjects. |
| `configured_marketplace_icon_path`, `user_visible_manifest_change` | Receive those Git-produced commit IDs and append fixed manifest paths (and the parent operator where needed). |

`git`, `source_notes`, and the commit inspection helpers are internal script
building blocks, not standalone validators for arbitrary external calls. This
review covers their current CLI-reachable callers, not a sandbox against a
malicious Git executable, Git configuration, repository, or future new caller.

The release workflow's `Refresh release notes` step selects reachable stable tags
with an anchored pattern and passes the result as one quoted argument. It runs
tooling from the separately checked-out trusted main revision. Python validation
still applies independently of that workflow filter.

## Executable evidence

```sh
go test ./scripts -run TestReleaseNotesRejectInvalidTagsBeforeSubprocess -count=1
python3 -B -m unittest discover -s scripts -p '*release_notes_test.py'
python3 -B scripts/release_build_notes_test.py
```

The Go test includes `release_notes_security_test.py` in the normal repository
suite. It executes both scripts' actual `__main__` paths with invalid CLI tags,
intercepts `subprocess.Popen`, and asserts the validation diagnostic, exit code
1, and zero subprocess calls. An empty repository ensures validation precedes
repository reads. Direct exported tag readers receive the same probes. Valid-tag
checks verify unchanged `git show` argument lists; a positive control proves the
subprocess interceptor observes a Git invocation. Existing fixture tests run real
Git and check successful release-note generation.

## Finding disposition

The reported CLI path is a false positive: rejected input cannot reach the sink,
and accepted tags cannot alter Git's argument structure. The tests preserve this
boundary without changing runtime behavior or weakening scanner configuration.
Mark the finding false positive with this evidence and verify a fresh main
analysis before claiming the gate is repaired. The three independent schema
identifier findings have evidence in [schema-identifier-evidence.md](schema-identifier-evidence.md).
