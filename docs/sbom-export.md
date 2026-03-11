# SBOM export

Lopper can export analysis results as Software Bill of Materials (SBOM) documents in CycloneDX and SPDX formats.

Supported formats:

- `cyclonedx-json`
- `cyclonedx-xml`
- `spdx-json`
- `spdx-tv` (SPDX tag-value)

## Examples

Generate CycloneDX JSON:

```bash
lopper analyse --top 50 --repo . --language all --format cyclonedx-json > lopper.cdx.json
```

Generate CycloneDX XML:

```bash
lopper analyse --top 50 --repo . --language all --format cyclonedx-xml > lopper.cdx.xml
```

Generate SPDX JSON:

```bash
lopper analyse --top 50 --repo . --language all --format spdx-json > lopper.spdx.json
```

Generate SPDX tag-value:

```bash
lopper analyse --top 50 --repo . --language all --format spdx-tv > lopper.spdx
```

## Metadata mapping

For each dependency component/package, Lopper maps:

- PURL by ecosystem (`npm`, `pypi`, `golang`, `maven`, etc.).
- SPDX license expression when known.
- SHA256 checksum when available from dependency provenance signals.
- Lopper analysis metadata:
  - `lopper:waste-score`
  - `lopper:used-percent`
  - `lopper:recommendation`

## Validation

CycloneDX validation (official CLI):

```bash
cyclonedx validate --input-file lopper.cdx.json
cyclonedx validate --input-file lopper.cdx.xml
```

SPDX validation (SPDX tools):

```bash
java -jar tools-java-<version>-jar-with-dependencies.jar Verify lopper.spdx.json
java -jar tools-java-<version>-jar-with-dependencies.jar Verify lopper.spdx
```

## CI example (GitHub Actions)

```yaml
name: sbom
on:
  push:
    branches: [main]
  pull_request:

jobs:
  sbom:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build lopper
        run: go build -o ./bin/lopper ./cmd/lopper
      - name: Export SBOMs
        run: |
          ./bin/lopper analyse --top 50 --repo . --language all --format cyclonedx-json > lopper.cdx.json
          ./bin/lopper analyse --top 50 --repo . --language all --format spdx-json > lopper.spdx.json
      - name: Upload artifacts
        uses: actions/upload-artifact@v4
        with:
          name: sbom
          path: |
            lopper.cdx.json
            lopper.spdx.json
```
