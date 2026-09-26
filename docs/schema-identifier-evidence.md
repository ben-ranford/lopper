# HTTPS schema locations and offline compatibility

Lopper emits HTTPS `$schema` locations in report and dashboard CycloneDX JSON
and Teams Adaptive Cards:

| Document | Emitted schema location |
| --- | --- |
| CycloneDX 1.6 report and portfolio | `https://cyclonedx.org/schema/bom-1.6.schema.json` |
| Teams Adaptive Card | `https://adaptivecards.io/schemas/adaptive-card.json` |

These replace the corresponding HTTP locations to resolve the three `go:S5332`
findings (`AaCKtNClot68TiTTknTM`, `AaCKtM4-ot68TiTTknTL`, and
`AaCKtNHiot68TiTTknTN`) in production code. Lopper still does not fetch schemas
when serializing these documents. Consumers that choose to dereference the
emitted locations now start with HTTPS.

## Published contracts

Both [CycloneDX](https://cyclonedx.org/schema/bom-1.6.schema.json) and
[Adaptive Cards](https://adaptivecards.io/schemas/adaptive-card.json) serve their
schemas over HTTPS, verified on 2026-09-24. The current Adaptive Cards endpoint
also declares the HTTPS address as its `id`.

The pinned CycloneDX 1.6 schema and Adaptive Cards 1.4 fixture retain their
original upstream HTTP identifiers and checksums. Those identifiers belong to
the schema documents themselves; neither schema requires the serialized
instance's `$schema` value to equal that identifier. CycloneDX permits a string,
and Adaptive Cards permits a URI. The emitted documents continue to validate
against those exact pinned versions with the HTTPS locations.

This is an intentional wire-value change from the earlier evidence-only PR
#1688. Consumers comparing `$schema` strings should accept the HTTPS locations.
The CycloneDX `specVersion` and Adaptive Card `version` remain unchanged.

## Executable evidence

```sh
go test ./scripts -run 'Test(SchemaIdentifiersRemainCompatibleWithoutNetwork|AdaptiveCardSchemaFixtureChecksum)'
go test ./internal/report -run TestCycloneDXSchema
```

`TestSchemaIdentifiersRemainCompatibleWithoutNetwork` checks that all three
serialized locations are the HTTPS counterparts of their pinned upstream
identifiers, then validates the complete documents using local schema bytes.
Default HTTP clients reject network access, and a negative control proves an
unregistered reference cannot silently fetch a schema. The Teams test captures
exactly one delivery POST to the configured HTTPS webhook using an in-memory
transport. Upstream fixture checksums remain unchanged.
