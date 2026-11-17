# Security Policy

## Supported versions

Security fixes are provided for the latest stable `1.8.x` release and for rolling builds from `main`.

| Channel | Supported |
| --- | --- |
| Latest `1.8.x` stable release | Yes |
| Rolling builds from `main` | Yes |
| Older releases | No |

Rolling builds track `main` and receive fixes as they land. They are not a second long-term support line. Older minor lines, including releases before `1.8`, are not supported.

## Reporting a vulnerability

Report suspected vulnerabilities through GitHub private vulnerability reporting:

<https://github.com/ben-ranford/lopper/security/advisories/new>

Do not open a public issue, pull request, or discussion for an unfixed vulnerability. If that private form is unavailable, contact the maintainer at <https://github.com/ben-ranford> and do not include exploit details in a public channel.

Please include the affected version or commit, the component, the impact, and a minimal reproduction. Say whether any part of the report is already public.

## Response targets

These are response targets, not a guarantee:

- Acknowledgement within 3 business days.
- An initial assessment within 7 days of acknowledgement.
- For critical or high severity, a fix or mitigation within 30 days of assessment.
- For medium or low severity, a fix in the next stable `1.8.x` release, or within 90 days of assessment, whichever comes first.

Accepted reports are coordinated through a GitHub security advisory. The maintainer will agree a disclosure time before publishing the advisory.

## Scope

Reports are in scope when they affect the Lopper CLI, language parsers, analysis pipeline, report and baseline writers, GitHub Action, VS Code extension, or the release automation in this repository. That includes:

- Parser denial of service: a manifest, lockfile, or other input that makes a normal scan use unbounded CPU, memory, or disk.
- Path containment: a read, write, archive, baseline, report output, or temporary worktree that escapes the repository root or another root the operator configured.
- Credential or token exposure through command output, logs, Action inputs, or published artifacts.
- Supply-chain issues in release archives, container images, or the VS Code extension package that originate in this repository.

A finding is out of scope when it is a vulnerability in a third-party dependency that Lopper only reports on, a low unused-dependency score with no defect in Lopper, or an issue that requires the operator to disable Lopper's own limits. Social engineering of maintainers is also out of scope for this process.
