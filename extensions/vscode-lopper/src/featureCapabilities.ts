export const sbomAttestationExportsFeature = "sbom-attestation-exports-preview";
export const reachabilityVulnerabilityFeature = "reachability-vulnerability-prioritization-preview";
export const pythonRunnerProfilesFeature = "python-runner-profiles";
export const vscodePreviewCapabilityParityFeature = "vscode-preview-capability-parity";

export type LopperFeatureOperation = "analysis" | "cyclonedx-export" | "python-runtime" | "runtime-test";
export type LopperFeatureLifecycle = "preview" | "stable";

export interface LopperFeatureManifestEntry {
  code: string;
  name: string;
  description: string;
  lifecycle: LopperFeatureLifecycle;
  enabledByDefault: boolean;
}

export interface LopperFeatureSettings {
  enable: readonly string[];
  disable: readonly string[];
}

export interface LopperFeatureResolutionRequest extends LopperFeatureSettings {
  operations: readonly LopperFeatureOperation[];
  required: readonly string[];
}

export interface LopperFeatureOverrides {
  enable: string[];
  disable: string[];
}

interface LopperFeatureCapability {
  readonly name: string;
  readonly operations: readonly LopperFeatureOperation[];
  readonly configurableEnable: boolean;
  readonly configurableDisable: boolean;
}

interface LopperOperationCapability {
  readonly requiredFeatures: readonly string[];
  readonly requiresWorkspaceTrust: boolean;
  readonly explicitUserAction: boolean;
}

const featureCapabilities: readonly LopperFeatureCapability[] = [
  {
    name: reachabilityVulnerabilityFeature,
    operations: ["analysis"],
    configurableEnable: true,
    configurableDisable: true,
  },
  {
    name: sbomAttestationExportsFeature,
    operations: ["cyclonedx-export"],
    configurableEnable: true,
    configurableDisable: true,
  },
  {
    name: pythonRunnerProfilesFeature,
    operations: ["runtime-test"],
    configurableEnable: true,
    configurableDisable: true,
  },
  {
    name: vscodePreviewCapabilityParityFeature,
    operations: ["cyclonedx-export", "python-runtime"],
    configurableEnable: false,
    configurableDisable: true,
  },
];

const operationCapabilities: Readonly<Record<LopperFeatureOperation, LopperOperationCapability>> = {
  analysis: {
    requiredFeatures: [],
    requiresWorkspaceTrust: false,
    explicitUserAction: false,
  },
  "cyclonedx-export": {
    requiredFeatures: [vscodePreviewCapabilityParityFeature, sbomAttestationExportsFeature],
    requiresWorkspaceTrust: false,
    explicitUserAction: true,
  },
  "python-runtime": {
    requiredFeatures: [vscodePreviewCapabilityParityFeature],
    requiresWorkspaceTrust: false,
    explicitUserAction: true,
  },
  "runtime-test": {
    requiredFeatures: [],
    requiresWorkspaceTrust: true,
    explicitUserAction: true,
  },
};

const featureCapabilityByName = new Map(featureCapabilities.map((capability) => [capability.name, capability]));

export const vscodeEnableFeatureNames = featureCapabilities
  .filter((capability) => capability.configurableEnable)
  .map((capability) => capability.name);

export const vscodeDisableFeatureNames = featureCapabilities
  .filter((capability) => capability.configurableDisable)
  .map((capability) => capability.name);

export function operationRequiresWorkspaceTrust(operation: LopperFeatureOperation): boolean {
  return operationCapabilities[operation].requiresWorkspaceTrust;
}

export function operationRequiresExplicitUserAction(operation: LopperFeatureOperation): boolean {
  return operationCapabilities[operation].explicitUserAction;
}

export function requiredFeaturesForOperation(operation: LopperFeatureOperation): readonly string[] {
  return operationCapabilities[operation].requiredFeatures;
}

export function parseFeatureManifest(output: string): LopperFeatureManifestEntry[] {
  let value: unknown;
  try {
    value = JSON.parse(output);
  } catch (error) {
    throw new Error(`invalid feature catalog JSON: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (!Array.isArray(value)) {
    throw new TypeError("invalid feature catalog JSON: expected an array");
  }

  const codes = new Set<string>();
  const names = new Set<string>();
  return value.map((entry, index) => {
    if (!isRecord(entry)) {
      throw new TypeError(`invalid feature catalog entry at index ${index}: expected an object`);
    }
    const code = requiredString(entry, "code", index);
    const name = requiredString(entry, "name", index);
    const description = optionalString(entry, "description", index);
    const lifecycle = entry.lifecycle;
    if (lifecycle !== "preview" && lifecycle !== "stable") {
      throw new TypeError(`invalid feature catalog entry ${name}: lifecycle must be preview or stable`);
    }
    if (typeof entry.enabledByDefault !== "boolean") {
      throw new TypeError(`invalid feature catalog entry ${name}: enabledByDefault must be boolean`);
    }
    if (codes.has(code)) {
      throw new Error(`invalid feature catalog JSON: duplicate feature code ${code}`);
    }
    if (names.has(name)) {
      throw new Error(`invalid feature catalog JSON: duplicate feature name ${name}`);
    }
    codes.add(code);
    names.add(name);
    return {
      code,
      name,
      description,
      lifecycle,
      enabledByDefault: entry.enabledByDefault,
    };
  });
}

export function resolveFeatureOverrides(
  manifest: readonly LopperFeatureManifestEntry[],
  request: LopperFeatureResolutionRequest,
): LopperFeatureOverrides {
  const enabledNames = normalizeFeatureNames(request.enable);
  const disabledNames = normalizeFeatureNames(request.disable);
  const requiredNames = normalizeFeatureNames(request.required);
  const manifestByName = new Map(manifest.map((entry) => [entry.name, entry]));

  validateConfiguredFeatureNames(enabledNames, manifestByName, "enable");
  validateConfiguredFeatureNames(disabledNames, manifestByName, "disable");
  validateRequiredFeatureNames(requiredNames, manifestByName);

  const activeOperations = new Set(request.operations);
  const disabled = new Set(disabledNames.filter((name) => appliesToOperation(name, activeOperations)));
  const enabled = new Set(
    enabledNames.filter((name) => appliesToOperation(name, activeOperations) && !disabled.has(name)),
  );

  for (const name of requiredNames) {
    if (disabled.has(name)) {
      throw new Error(`Lopper feature ${name} is disabled but required for this operation.`);
    }
    const entry = manifestByName.get(name);
    if (entry?.enabledByDefault === false && entry.lifecycle === "stable") {
      throw new Error(
        `Lopper stable feature ${name} is disabled by the selected binary but required for this operation. Remove its rollback configuration before retrying.`,
      );
    }
    if (!entry?.enabledByDefault) {
      enabled.add(name);
    }
  }

  return {
    enable: [...enabled].sort((a, b) => a.localeCompare(b, "en")),
    disable: [...disabled].sort((a, b) => a.localeCompare(b, "en")),
  };
}

function normalizeFeatureNames(values: readonly string[]): string[] {
  const names = new Set<string>();
  for (const value of values) {
    const name = value.trim();
    if (name.length > 0) {
      names.add(name);
    }
  }
  return [...names];
}

function validateConfiguredFeatureNames(
  names: readonly string[],
  manifestByName: ReadonlyMap<string, LopperFeatureManifestEntry>,
  override: "enable" | "disable",
): void {
  for (const name of names) {
    const capability = featureCapabilityByName.get(name);
    const configurable = override === "enable"
      ? capability?.configurableEnable
      : capability?.configurableDisable;
    if (configurable !== true) {
      throw new Error(
        `Lopper feature ${name} is not available to VS Code's ${override} setting. Choose an allowlisted feature from the workspace settings UI.`,
      );
    }
    if (!manifestByName.has(name)) {
      throw staleBinaryFeatureError(name);
    }
  }
}

function validateRequiredFeatureNames(
  names: readonly string[],
  manifestByName: ReadonlyMap<string, LopperFeatureManifestEntry>,
): void {
  for (const name of names) {
    if (!featureCapabilityByName.has(name)) {
      throw new Error(`Lopper operation requires an unregistered VS Code feature capability: ${name}.`);
    }
    if (!manifestByName.has(name)) {
      throw staleBinaryFeatureError(name);
    }
  }
}

function staleBinaryFeatureError(name: string): Error {
  return new Error(
    `The selected lopper binary does not report feature ${name}. Update lopper.binaryPath or lopper.managedBinaryTag, or remove the unsupported setting.`,
  );
}

function appliesToOperation(name: string, activeOperations: ReadonlySet<LopperFeatureOperation>): boolean {
  const capability = featureCapabilityByName.get(name);
  return capability?.operations.some((operation) => activeOperations.has(operation)) === true;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function requiredString(entry: Record<string, unknown>, field: string, index: number): string {
  const value = entry[field];
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new TypeError(`invalid feature catalog entry at index ${index}: ${field} must be a non-empty string`);
  }
  return value.trim();
}

function optionalString(entry: Record<string, unknown>, field: string, index: number): string {
  const value = entry[field];
  if (value === undefined) {
    return "";
  }
  if (typeof value !== "string") {
    throw new TypeError(`invalid feature catalog entry at index ${index}: ${field} must be a string`);
  }
  return value.trim();
}
