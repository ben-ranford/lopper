#!/usr/bin/env node
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = process.argv.slice(2);
const command = args[0];
const options = parseArgs(args.slice(1));
const cwd = process.cwd();
const format = options.format ?? "json";

if (command === "features") {
  process.stdout.write(JSON.stringify(featureManifest(), null, 2));
  process.exit(0);
}

if (command !== "analyse") {
  process.stdout.write(renderExport(command, options, cwd));
  process.exit(0);
}

if (
  options.language === "python"
  && (options["runtime-trace"] || options["runtime-test-command"])
  && !(options["enable-feature"] ?? []).includes("vscode-preview-capability-parity")
) {
  process.stderr.write("python runtime parity requires vscode-preview-capability-parity\n");
  process.exit(2);
}

if (format !== "json") {
  process.stdout.write(renderExport(command, options, cwd));
  process.exit(0);
}

const dependencyName = options._[0] ?? "scope-lib";
const suggestOnly = options["suggest-only"] !== undefined;
const applyCodemod = options["apply-codemod"] !== undefined;
const report = await buildReport(cwd, dependencyName, suggestOnly, applyCodemod);
process.stdout.write(JSON.stringify(report, null, 2));

function parseArgs(tokens) {
  const parsed = { _: [] };
  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token === "--") {
      parsed._.push(...tokens.slice(index + 1));
      break;
    }
    if (token.startsWith("--")) {
      const key = token.slice(2);
      const next = tokens[index + 1];
      if (next !== undefined && !next.startsWith("--")) {
        if (key === "enable-feature" || key === "disable-feature") {
          parsed[key] = [...(parsed[key] ?? []), next];
        } else {
          parsed[key] = next;
        }
        index += 1;
      } else {
        parsed[key] = "";
      }
      continue;
    }
    parsed._.push(token);
  }
  return parsed;
}

async function buildReport(workspaceRoot, dependencyName, suggestOnly, applyCodemod) {
  const indexPath = path.join(workspaceRoot, "src", "index.ts");
  const source = await readFile(indexPath, "utf8");
  const lines = source.split(/\r?\n/);
  const chunkLine = lines.findIndex((line) => line.includes('import { chunk } from "scope-lib";')) + 1;
  const idleLine = lines.findIndex((line) => line.includes('import { idle } from "scope-lib";')) + 1;
  const analysis = {
    schemaVersion: "1.0.0",
    generatedAt: new Date().toISOString(),
    repoPath: workspaceRoot,
    scope: {
      mode: "package",
      packages: [path.basename(workspaceRoot)],
    },
    summary: {
      dependencyCount: 1,
      usedPercent: 50,
      usedExportsCount: 1,
      totalExportsCount: 2,
      knownLicenseCount: 0,
      unknownLicenseCount: 1,
      deniedLicenseCount: 0,
      reachability: {
        model: "smoke",
        averageScore: 0.5,
        lowestScore: 0.5,
        highestScore: 0.5,
      },
    },
    dependencies: [
      {
        language: "js-ts",
        name: dependencyName,
        usedExportsCount: 1,
        totalExportsCount: 2,
        usedPercent: 50,
        estimatedUnusedBytes: 128,
        topUsedSymbols: [{ name: "chunk", module: "scope-lib", count: 1 }],
        riskCues: [
          {
            code: "smoke-risk",
            severity: "medium",
            message: "Unused import keeps the dependency visible in the smoke fixture.",
          },
        ],
        recommendations: [
          {
            code: "smoke-rec",
            priority: "normal",
            message: "Prefer the scoped subpath import for direct use of chunk.",
          },
        ],
        usedImports: [
          {
            name: "chunk",
            module: "scope-lib",
            locations: [{ file: "src/index.ts", line: chunkLine, column: 10 }],
            provenance: ["static"],
            confidenceScore: 0.98,
            confidenceReasonCodes: ["static-import"],
          },
        ],
        unusedImports: [
          {
            name: "idle",
            module: "scope-lib",
            locations: [{ file: "src/index.ts", line: idleLine, column: 10 }],
            provenance: ["static"],
            confidenceScore: 0.25,
            confidenceReasonCodes: ["unused-symbol"],
          },
        ],
        unusedExports: [{ name: "idle", module: "scope-lib" }],
        runtimeUsage: {
          loadCount: 1,
          correlation: "overlap",
          runtimeOnly: false,
          modules: [{ module: "scope-lib", count: 1 }],
          topSymbols: [{ symbol: "chunk", module: "scope-lib", count: 1 }],
        },
        reachabilityConfidence: {
          model: "smoke",
          score: 0.5,
          summary: "moderate",
          rationaleCodes: ["static-analysis"],
          signals: [
            {
              code: "smoke",
              score: 0.5,
              weight: 1,
              contribution: 0.5,
            },
          ],
        },
        removalCandidate: {
          score: 0.5,
          usage: 0.5,
          impact: 0.5,
          confidence: 0.5,
          weights: {
            usage: 0.4,
            impact: 0.3,
            confidence: 0.3,
          },
          rationale: ["smoke fixture"],
        },
        license: {
          unknown: true,
          raw: "unknown",
          source: "smoke-fixture",
          confidence: "low",
          evidence: ["package.json"],
        },
        provenance: {
          source: "unknown",
          confidence: "low",
          signals: ["registry metadata unavailable"],
        },
      },
    ],
    usageUncertainty: {
      confirmedImportUses: 1,
      uncertainImportUses: 1,
      samples: [{ file: "src/index.ts", line: idleLine, column: 10 }],
    },
    languageBreakdown: [
      {
        language: "js-ts",
        dependencyCount: 1,
        usedExportsCount: 1,
        totalExportsCount: 2,
        usedPercent: 50,
      },
    ],
    cache: {
      enabled: false,
      hits: 0,
      misses: 1,
      writes: 0,
    },
    effectiveThresholds: {
      failOnIncreasePercent: -1,
      lowConfidenceWarningPercent: 40,
      minUsagePercentForRecommendations: 40,
      maxUncertainImportCount: -1,
    },
    effectivePolicy: {
      sources: ["smoke-fixture"],
      thresholds: {
        failOnIncreasePercent: -1,
        lowConfidenceWarningPercent: 40,
        minUsagePercentForRecommendations: 40,
        maxUncertainImportCount: -1,
      },
      removalCandidateWeights: {
        usage: 0.4,
        impact: 0.3,
        confidence: 0.3,
      },
      license: {
        deny: [],
        failOnDenied: false,
        includeRegistryProvenance: false,
      },
    },
    warnings: suggestOnly ? ["suggest-only codemod lookup"] : [],
    wasteIncreasePercent: 0,
  };

  if (suggestOnly || applyCodemod) {
    const suggestion = {
      file: "src/index.ts",
      line: 1,
      importName: "chunk",
      fromModule: "scope-lib",
      toModule: "scope-lib/chunk",
      original: 'import { chunk } from "scope-lib";',
      replacement: 'import chunk from "scope-lib/chunk";',
      patch:
        '--- a/src/index.ts\n+++ b/src/index.ts\n@@ -1 +1 @@\n-import { chunk } from "scope-lib";\n+import chunk from "scope-lib/chunk";',
    };
    const codemod = {
      mode: applyCodemod ? "apply" : "suggest-only",
      suggestions: [suggestion],
    };
    if (applyCodemod) {
      codemod.apply = await applySmokeCodemod(workspaceRoot, indexPath, source, dependencyName, suggestion);
    }
    analysis.dependencies = [
      {
        language: "js-ts",
        name: dependencyName,
        usedExportsCount: 1,
        totalExportsCount: 2,
        usedPercent: 50,
        codemod,
      },
    ];
  }

  return analysis;
}

async function applySmokeCodemod(workspaceRoot, indexPath, source, dependencyName, suggestion) {
  if (!source.includes(suggestion.original)) {
    return {
      failedFiles: 1,
      failedPatches: 1,
      results: [{ file: suggestion.file, status: "failed", patchCount: 1, message: "original line mismatch" }],
    };
  }

  const backupPath = ".artifacts/lopper-codemod-backups/scope-lib-smoke.json";
  await mkdir(path.dirname(path.join(workspaceRoot, backupPath)), { recursive: true });
  await writeFile(
    path.join(workspaceRoot, backupPath),
    JSON.stringify(
      {
        generatedAt: new Date().toISOString(),
        dependency: dependencyName,
        files: [{ file: suggestion.file, mode: 0o644, content: source }],
      },
      null,
      2,
    ),
    "utf8",
  );
  await writeFile(indexPath, source.replace(suggestion.original, suggestion.replacement), "utf8");
  return {
    appliedFiles: 1,
    appliedPatches: 1,
    skippedFiles: 0,
    skippedPatches: 0,
    failedFiles: 0,
    failedPatches: 0,
    backupPath,
    results: [{ file: suggestion.file, status: "applied", patchCount: 1 }],
  };
}

function renderExport(commandName, parsedOptions, workspaceRoot) {
  const format = parsedOptions.format ?? "json";
  const rows = [
    ["dependency", "used_exports", "total_exports", "used_percent"],
    ["scope-lib", "1", "2", "50.0"],
  ];

  if (format === "csv") {
    return rows.map((row) => row.map(escapeCsv).join(",")).join("\n") + "\n";
  }

  if (format === "sarif") {
    return JSON.stringify(
      {
        version: "2.1.0",
        runs: [
          {
            tool: {
              driver: {
                name: "lopper-smoke",
                rules: [],
              },
            },
            results: [],
          },
        ],
      },
      null,
      2,
    );
  }

  if (format === "pr-comment") {
    return [
      `# Lopper export for ${path.basename(workspaceRoot)}`,
      "",
      "- scope-lib: 1/2 exports used",
      "",
    ].join("\n");
  }

  if (format === "cyclonedx-json") {
    const enabledFeatures = parsedOptions["enable-feature"] ?? [];
    if (
      !enabledFeatures.includes("sbom-attestation-exports-preview")
      || !enabledFeatures.includes("vscode-preview-capability-parity")
    ) {
      process.stderr.write("cyclonedx-json requires SBOM and VS Code parity capabilities\n");
      process.exit(2);
    }
    return JSON.stringify(
      {
        bomFormat: "CycloneDX",
        specVersion: "1.6",
        serialNumber: "urn:uuid:00000000-0000-4000-8000-000000000001",
        version: 1,
        metadata: {
          component: {
            type: "application",
            name: path.basename(workspaceRoot),
          },
        },
        components: [
          {
            type: "library",
            name: "scope-lib",
            properties: [{ name: "lopper:usedPercent", value: "50.0" }],
          },
        ],
      },
      null,
      2,
    );
  }

  return JSON.stringify(
    {
      command: commandName,
      format,
      workspaceRoot,
    },
    null,
    2,
  );
}

function featureManifest() {
  return [
    {
      code: "LOP-FEAT-0013",
      name: "sbom-attestation-exports-preview",
      description: "Enable preview CycloneDX SBOM exports with Lopper dependency-surface metadata.",
      lifecycle: "preview",
      enabledByDefault: false,
    },
    {
      code: "LOP-FEAT-0015",
      name: "reachability-vulnerability-prioritization-preview",
      description: "Enable local advisory ingestion and reachability-weighted vulnerability prioritization.",
      lifecycle: "preview",
      enabledByDefault: false,
    },
    {
      code: "LOP-FEAT-0018",
      name: "python-runner-profiles",
      description: "Enable safe unittest and uv profiles for Python runtime capture.",
      lifecycle: "preview",
      enabledByDefault: false,
    },
    {
      code: "LOP-FEAT-0020",
      name: "vscode-preview-capability-parity",
      description: "Enable VS Code controls for safe CLI preview capabilities.",
      lifecycle: "preview",
      enabledByDefault: false,
    },
  ];
}

function escapeCsv(value) {
  const text = String(value);
  if (/[",\n]/.test(text)) {
    return `"${text.replaceAll('"', '""')}"`;
  }
  return text;
}
