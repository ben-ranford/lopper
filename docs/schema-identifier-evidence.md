# Schema identifier transport evidence

Issue #1649 concerns three `go:S5332` findings:

| Finding | Serialized field |
| --- | --- |
| `AaCKtNClot68TiTTknTM` | Dashboard CycloneDX `$schema` |
| `AaCKtM4-ot68TiTTknTL` | Report CycloneDX `$schema` |
| `AaCKtNHiot68TiTTknTN` | Teams Adaptive Card `$schema` |

These fields contain published schema identifiers. They are assigned to JSON
fields and serialized; none is used as a transport destination. The identifiers
remain unchanged, with no scanner suppression, exclusion, or string obfuscation.

## Published contracts

The pinned [CycloneDX 1.6 schema](https://github.com/CycloneDX/specification/blob/55343ba19dee1785acf1ce9191540d5fd7b590db/schema/bom-1.6.schema.json)
declares `$id` as `http://cyclonedx.org/schema/bom-1.6.schema.json`. The repository
already vendors that schema and its referenced schemas with checked checksums.

The pinned [Adaptive Cards 1.4 schema](https://github.com/microsoft/AdaptiveCards/blob/93695cf2df1117ab08e36ebe707d0ced18e4e9b3/schemas/1.4.0/adaptive-card.json)
declares `id` as `http://adaptivecards.io/schemas/adaptive-card.json`.
[Microsoft's AdaptiveCard examples](https://learn.microsoft.com/en-us/adaptive-cards/schema-explorer/adaptive-card)
use that exact value in `$schema`. The unchanged upstream schema, license, and
provenance are checked into `testdata/notify/adaptive-cards-1.4`.

## Rule scope and evidence limits

The [SonarSource Go rule API](https://next.sonarqube.com/sonarqube/api/rules/show?key=go%3AS5332),
retrieved on 2026-09-23, identifies `go:S5332` as "Clear-text protocols should not
be used", with type `VULNERABILITY` and the `former-hotspot` system tag. Its
`updatedAt` value is `2026-09-22T10:24:19+0000`. The response does not include a
detailed rule description, so this metadata establishes the rule's identity and
classification, not the Go analyzer's complete detection or exception behavior.

The [Java S5332 documentation](https://rules.sonarsource.com/java/RSPEC-5332/)
provides supporting context for the transport-security concern only. It is
cross-language evidence, not proof that the Go analyzer implements identical
behavior or exceptions. The disposition of these three Go findings rests on the
pinned published schema identifiers above and the local data-flow and executable
evidence below.

## Executable evidence

Run:

```sh
go test ./scripts -run 'Test(SchemaIdentifiersRemainCompatibleWithoutNetwork|AdaptiveCardSchemaFixtureChecksum)'
go test ./internal/report -run TestCycloneDXSchema
```

`TestSchemaIdentifiersRemainCompatibleWithoutNetwork` serializes a report, a
dashboard portfolio, and a Teams webhook delivery. It checks each `$schema`
against the identifier in the pinned upstream schema, then validates the entire
document using the existing JSON Schema validator and only local schema bytes.
Both default HTTP client and default HTTP transport reject network access. A
negative control attempts to load an unregistered schema URI and proves the
transport rejects that fetch.

The webhook uses an injected in-memory transport: exactly one POST targets the
configured HTTPS webhook endpoint, while its Adaptive Card retains the HTTP
schema identifier. No network connection is made. This distinguishes the
configured delivery destination from the schema identifier in its JSON body.

## Finding disposition

The tests and source data flow support marking these three transport findings as
false positives, rather than changing the wire format. This document does not
claim that a Sonar administrator has performed that transition. Record the
evidence-linked disposition in Sonar and verify a fresh analysis before treating
the findings as resolved. Other HTTP transport findings require separate review.
