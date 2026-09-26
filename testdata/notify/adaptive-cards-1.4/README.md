# Pinned Adaptive Cards 1.4 schema

`adaptive-card.json` is an unchanged copy of the Microsoft Adaptive Cards schema:

- Repository: https://github.com/microsoft/AdaptiveCards
- Commit: `93695cf2df1117ab08e36ebe707d0ced18e4e9b3`
- Path: `schemas/1.4.0/adaptive-card.json`
- Git blob: `a65f7af69e84f7fddd39880a29115da427ed5e58`
- SHA-256: `e1750b01c13459b1d937ae0a8792d825314d54ddd22b1c8159cfd809d2caab09`
- License: MIT; upstream `LICENSE` is included.

The schema uses internal references only. Tests load it from disk, block HTTP
transports, and verify its checksum; they do not download schema documents.
