# Release-note subprocess validation evidence

Issue #1648 tracks SonarCloud finding `AaCK61tceMZvS3_EWBJE`
(`pythonsecurity:S8705`). The reported flow is CLI `--previous-tag` through
`release_build_notes.generate` into `vscode_release_notes.git`.

## Boundary and caller audit

Both CLI entry points parse the tag as a string and call their `generate`
function. Each generator rejects it with `STABLE_TAG.fullmatch` before any
repository reads or Git calls. The expression is
`v\d{1,9}\.\d{1,9}\.\d{1,9}\Z`: it requires a leading `v`, three bounded digit
components, and the end of the string. Python's `\d` also permits Unicode decimal
digits; these remain literal revision characters and cannot introduce options,
revision operators, a colon, whitespace, or shell syntax.

Every production caller of the exported `git` helper is accounted for:

| Caller | Arguments reaching Git | Validation / origin |
| --- | --- | --- |
| `release_build_notes.generate` | `show`, `<tag>:go.mod` | Full-match validation at generator entry; constant path |
| `old_lockfile` | `show`, `<tag>:extensions/vscode-lopper/package-lock.json` | Its own full-match validation; constant path |
| `old_package` | `show`, `<tag>:extensions/vscode-lopper/package.json` | Its own full-match validation; constant path |
| `visible_extension_commits` | `log`, fixed format, `<tag>..HEAD`, `--`, fixed extension path; then `diff-tree` with each SHA | Called by `source_notes` from the validated VS Code generator; SHA comes from Git's `%H` output |
| `configured_marketplace_icon_path` | `show`, `<sha>:extensions/vscode-lopper/package.json` | SHA from the same Git log; constant path |
| `user_visible_manifest_change` | `show`, `<sha>^:extensions/vscode-lopper/package.json` and current object | SHA from the same Git log; constant path |

The generic `git` helper and internal traversal helpers are not standalone input
validators. Their production paths above establish the boundary. Callers adding a
new external input must validate it before invoking them. Git runs with an
argument list, default `shell=False`, and an explicit `-C` repository argument.
Shell quoting would change argument values rather than improve this API.

The only release workflow invocation is the `Refresh release notes` step in
`.github/workflows/release.yml`. It selects a reachable tag with
`git tag --merged HEAD --sort=-version:refname`, filters stable tag syntax, and
passes the result as a quoted shell variable. Both Python validators independently
check the tag again. The scripts are loaded from the separate trusted tooling
checkout pinned to the workflow's validated full SHA; the release PR checkout
provides repository data. The `--check` invocation does not execute Git.

## Executable proof

Run:

```sh
python3 -m unittest scripts/vscode_release_notes_test.py scripts/release_build_notes_test.py
```

Each suite's `test_cli_rejects_invalid_tags_before_starting_subprocess` calls the
real argument parser and CLI handler with malformed tags, including option-like
input, shell syntax, a trailing newline, revision/path syntax, prereleases and
oversized components. The test patches `subprocess.Popen`, the process creation
boundary beneath `check_output`, and asserts it was never called. Existing
real-Git generation tests prove stable tags still read the historical files and
produce the expected release notes. The exported `old_lockfile` and `old_package`
readers also have direct rejection probes.

This evidence supports a false-positive disposition for the reported tag flow;
it does not assert that SonarCloud has recorded that disposition. After review,
record the justified disposition on the finding and verify its absence in a
fresh main-branch analysis. PR-only analysis is insufficient to close that check.
The schema findings in #1649 require their own evidence and disposition.
