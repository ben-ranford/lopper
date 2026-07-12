import * as assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import * as path from "node:path";
import { suite, test } from "mocha";

import {
  operationRequiresExplicitUserAction,
  operationRequiresWorkspaceTrust,
  parseFeatureManifest,
  pythonRunnerProfilesFeature,
  reachabilityVulnerabilityFeature,
  requiredFeaturesForOperation,
  resolveFeatureOverrides,
  sbomAttestationExportsFeature,
  vscodePreviewCapabilityParityFeature,
  vscodeDisableFeatureNames,
  vscodeEnableFeatureNames,
  type LopperFeatureManifestEntry,
} from "../../featureCapabilities";

suite("feature capabilities", () => {
  test("parses the CLI feature manifest strictly", () => {
    const manifest = parseFeatureManifest(JSON.stringify(featureManifest()));
    assert.deepEqual(manifest.map((entry) => entry.name), [
      sbomAttestationExportsFeature,
      reachabilityVulnerabilityFeature,
      pythonRunnerProfilesFeature,
      vscodePreviewCapabilityParityFeature,
    ]);

    assert.throws(() => parseFeatureManifest("{}"), {
      name: "TypeError",
      message: /expected an array/,
    });
    assert.throws(() => parseFeatureManifest("[null]"), {
      name: "TypeError",
      message: /expected an object/,
    });
    assert.throws(
      () => parseFeatureManifest(JSON.stringify([{ ...featureManifest()[0], code: "" }])),
      { name: "TypeError", message: /code must be a non-empty string/ },
    );
    assert.throws(
      () => parseFeatureManifest(JSON.stringify([{ ...featureManifest()[0], description: false }])),
      { name: "TypeError", message: /description must be a string/ },
    );
    assert.throws(
      () => parseFeatureManifest(JSON.stringify([{ ...featureManifest()[0], lifecycle: "experimental" }])),
      { name: "TypeError", message: /lifecycle must be preview or stable/ },
    );
    assert.throws(
      () => parseFeatureManifest(JSON.stringify([...featureManifest(), featureManifest()[0]])),
      /duplicate feature code/,
    );
    assert.throws(
      () => parseFeatureManifest(JSON.stringify([{ ...featureManifest()[0], enabledByDefault: "no" }])),
      { name: "TypeError", message: /enabledByDefault must be boolean/ },
    );
  });

  test("applies deterministic disable precedence and operation scoping", () => {
    const overrides = resolveFeatureOverrides(featureManifest(), {
      enable: [
        pythonRunnerProfilesFeature,
        reachabilityVulnerabilityFeature,
        sbomAttestationExportsFeature,
        pythonRunnerProfilesFeature,
      ],
      disable: [reachabilityVulnerabilityFeature, reachabilityVulnerabilityFeature],
      operations: ["analysis", "runtime-test", "cyclonedx-export"],
      required: [sbomAttestationExportsFeature],
    });

    assert.deepEqual(overrides, {
      enable: [pythonRunnerProfilesFeature, sbomAttestationExportsFeature],
      disable: [reachabilityVulnerabilityFeature],
    });

    assert.deepEqual(resolveFeatureOverrides(featureManifest(), {
      enable: [pythonRunnerProfilesFeature],
      disable: [],
      operations: ["analysis"],
      required: [],
    }), { enable: [], disable: [] });
  });

  test("does not redundantly enable a required feature already enabled by the CLI", () => {
    const manifest = featureManifest().map((entry) => entry.name === sbomAttestationExportsFeature
      ? { ...entry, lifecycle: "stable" as const, enabledByDefault: true }
      : entry);
    assert.deepEqual(resolveFeatureOverrides(manifest, {
      enable: [],
      disable: [],
      operations: ["analysis", "cyclonedx-export"],
      required: [sbomAttestationExportsFeature],
    }), { enable: [], disable: [] });
  });

  test("rejects disabled requirements, arbitrary flags, and stale binaries", () => {
    assert.throws(() => resolveFeatureOverrides(featureManifest(), {
      enable: [],
      disable: [sbomAttestationExportsFeature],
      operations: ["analysis", "cyclonedx-export"],
      required: [sbomAttestationExportsFeature],
    }), /disabled but required/);

    assert.throws(() => resolveFeatureOverrides(featureManifest(), {
      enable: ["mcp-mutation-tools"],
      disable: [],
      operations: ["analysis"],
      required: [],
    }), /not available to VS Code/);

    assert.throws(() => resolveFeatureOverrides(featureManifest(), {
      enable: [vscodePreviewCapabilityParityFeature],
      disable: [],
      operations: ["cyclonedx-export"],
      required: requiredFeaturesForOperation("cyclonedx-export"),
    }), /not available to VS Code's enable setting/);

    assert.throws(() => resolveFeatureOverrides(featureManifest(), {
      enable: [],
      disable: [vscodePreviewCapabilityParityFeature],
      operations: ["cyclonedx-export"],
      required: requiredFeaturesForOperation("cyclonedx-export"),
    }), /disabled but required/);

    assert.throws(() => resolveFeatureOverrides(featureManifest().slice(1), {
      enable: [sbomAttestationExportsFeature],
      disable: [],
      operations: ["cyclonedx-export"],
      required: [],
    }), /selected lopper binary does not report feature/);
  });

  test("marks only explicit test execution as trust-requiring", () => {
    assert.equal(operationRequiresWorkspaceTrust("analysis"), false);
    assert.equal(operationRequiresWorkspaceTrust("cyclonedx-export"), false);
    assert.equal(operationRequiresWorkspaceTrust("python-runtime"), false);
    assert.equal(operationRequiresWorkspaceTrust("runtime-test"), true);
    assert.equal(operationRequiresExplicitUserAction("cyclonedx-export"), true);
    assert.equal(operationRequiresExplicitUserAction("python-runtime"), true);
    assert.equal(operationRequiresExplicitUserAction("runtime-test"), true);
    assert.deepEqual(requiredFeaturesForOperation("cyclonedx-export"), [
      vscodePreviewCapabilityParityFeature,
      sbomAttestationExportsFeature,
    ]);
    assert.deepEqual(requiredFeaturesForOperation("python-runtime"), [vscodePreviewCapabilityParityFeature]);
  });

  test("uses stable VS Code parity without redundant flags across channels", () => {
    for (const channel of ["dev", "release"] as const) {
      const overrides = resolveFeatureOverrides(featureManifest(false), {
        enable: [],
        disable: [],
        operations: ["cyclonedx-export"],
        required: requiredFeaturesForOperation("cyclonedx-export"),
      });
      assert.deepEqual(overrides.enable, [sbomAttestationExportsFeature], channel);
    }

    const rollingOverrides = resolveFeatureOverrides(featureManifest(true), {
      enable: [],
      disable: [],
      operations: ["cyclonedx-export"],
      required: requiredFeaturesForOperation("cyclonedx-export"),
    });
    assert.deepEqual(rollingOverrides.enable, []);
  });

  test("does not override a selected binary that reports stable VS Code parity disabled", () => {
    const manifest = featureManifest().map((entry) => entry.name === vscodePreviewCapabilityParityFeature
      ? { ...entry, enabledByDefault: false }
      : entry);
    assert.throws(() => resolveFeatureOverrides(manifest, {
      enable: [],
      disable: [],
      operations: ["python-runtime"],
      required: requiredFeaturesForOperation("python-runtime"),
    }), /stable feature vscode-preview-capability-parity is disabled by the selected binary/);
  });

  test("preserves explicit rollback for stable Python runner profiles", () => {
    assert.deepEqual(resolveFeatureOverrides(featureManifest(), {
      enable: [],
      disable: [pythonRunnerProfilesFeature],
      operations: ["analysis", "runtime-test"],
      required: [],
    }), {
      enable: [],
      disable: [pythonRunnerProfilesFeature],
    });
  });

  test("keeps package setting enums aligned with the capability allowlist", async () => {
    const extensionRoot = path.resolve(__dirname, "../../..");
    const packageJson = JSON.parse(await readFile(path.join(extensionRoot, "package.json"), "utf8")) as {
      capabilities: {
        untrustedWorkspaces: {
          restrictedConfigurations?: string[];
        };
      };
      contributes: {
        configuration: {
          properties: Record<string, { items?: { enum?: string[] } }>;
        };
      };
    };
    const properties = packageJson.contributes.configuration.properties;
    assert.deepEqual(
      [...(properties["lopper.enableFeatures"].items?.enum ?? [])].sort(),
      [...vscodeEnableFeatureNames].sort(),
    );
    assert.deepEqual(
      [...(properties["lopper.disableFeatures"].items?.enum ?? [])].sort(),
      [...vscodeDisableFeatureNames].sort(),
    );
    assert.ok(
      packageJson.capabilities.untrustedWorkspaces.restrictedConfigurations?.includes("lopper.binaryPath"),
      "workspace-selected executable path must be declared restricted",
    );
  });
});

function featureManifest(previewEnabledByDefault = false): LopperFeatureManifestEntry[] {
  return [
    {
      code: "LOP-FEAT-0013",
      name: sbomAttestationExportsFeature,
      description: "CycloneDX",
      lifecycle: "preview",
      enabledByDefault: previewEnabledByDefault,
    },
    {
      code: "LOP-FEAT-0015",
      name: reachabilityVulnerabilityFeature,
      description: "Vulnerability priority",
      lifecycle: "preview",
      enabledByDefault: previewEnabledByDefault,
    },
    {
      code: "LOP-FEAT-0018",
      name: pythonRunnerProfilesFeature,
      description: "Python runners",
      lifecycle: "stable",
      enabledByDefault: true,
    },
    {
      code: "LOP-FEAT-0020",
      name: vscodePreviewCapabilityParityFeature,
      description: "VS Code parity",
      lifecycle: "stable",
      enabledByDefault: true,
    },
  ];
}
