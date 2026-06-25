export const lopperScopeModeValues = ["package", "repo", "changed-packages"] as const;
export type LopperScopeMode = typeof lopperScopeModeValues[number];

export interface LopperReport {
  schemaVersion?: string;
  generatedAt?: string;
  repoPath?: string;
  scope?: LopperScopeMetadata;
  summary?: LopperSummary;
  dependencies: LopperDependencyReport[];
  usageUncertainty?: LopperUsageUncertainty;
  languageBreakdown?: LopperLanguageSummary[];
  cache?: LopperCacheMetadata;
  effectiveThresholds?: LopperEffectiveThresholds;
  effectivePolicy?: LopperEffectivePolicy;
  warnings?: string[];
  wasteIncreasePercent?: number;
  baselineComparison?: LopperBaselineComparison;
}

export interface LopperSummary {
  dependencyCount: number;
  usedPercent: number;
  usedExportsCount?: number;
  totalExportsCount?: number;
  knownLicenseCount?: number;
  unknownLicenseCount?: number;
  deniedLicenseCount?: number;
  reachability?: LopperReachabilityRollup;
  vulnerabilities?: LopperVulnerabilitySummary;
}

export interface LopperScopeMetadata {
  mode: string;
  packages?: string[];
}

export interface LopperUsageUncertainty {
  confirmedImportUses: number;
  uncertainImportUses: number;
  samples?: LopperLocation[];
}

export interface LopperLanguageSummary {
  language: string;
  dependencyCount: number;
  usedExportsCount: number;
  totalExportsCount: number;
  usedPercent: number;
}

export interface LopperCacheMetadata {
  enabled: boolean;
  path?: string;
  readOnly?: boolean;
  hits: number;
  misses: number;
  writes: number;
  invalidations?: LopperCacheInvalidation[];
}

export interface LopperCacheInvalidation {
  key: string;
  reason: string;
}

export interface LopperEffectiveThresholds {
  failOnIncreasePercent: number;
  lowConfidenceWarningPercent: number;
  minUsagePercentForRecommendations: number;
  maxUncertainImportCount: number;
  reachableVulnerabilityPriority?: LopperVulnerabilityPriorityThreshold;
}

export interface LopperEffectivePolicy {
  sources?: string[];
  thresholds: LopperEffectiveThresholds;
  removalCandidateWeights: LopperRemovalCandidateWeights;
  license: LopperLicensePolicy;
  vulnerabilities?: LopperVulnerabilityPolicy;
}

export interface LopperRemovalCandidateWeights {
  usage: number;
  impact: number;
  confidence: number;
}

export interface LopperLicensePolicy {
  deny?: string[];
  failOnDenied: boolean;
  includeRegistryProvenance: boolean;
}

export interface LopperReachabilityRollup {
  model: string;
  averageScore?: number;
  lowestScore?: number;
  highestScore?: number;
}

export type LopperVulnerabilityPriority = "critical" | "high" | "medium" | "low";
export type LopperVulnerabilityPriorityThreshold = "off" | LopperVulnerabilityPriority;

export interface LopperVulnerabilityPolicy {
  advisorySourcePath?: string;
  reachablePriorityThreshold?: LopperVulnerabilityPriorityThreshold;
}

export interface LopperVulnerabilitySummary {
  totalFindings: number;
  reachableFindings: number;
  highestSeverity?: string;
  highestPriority?: LopperVulnerabilityPriority;
  bySeverity?: Record<string, number>;
  byPriority?: Record<string, number>;
  sources?: string[];
}

export interface LopperDependencyReport {
  language?: string;
  name: string;
  usedExportsCount: number;
  totalExportsCount: number;
  usedPercent: number;
  estimatedUnusedBytes?: number;
  topUsedSymbols?: LopperSymbolUsage[];
  riskCues?: LopperRiskCue[];
  recommendations?: LopperRecommendation[];
  usedImports?: LopperImportUse[];
  unusedImports?: LopperImportUse[];
  unusedExports?: LopperSymbolRef[];
  codemod?: LopperCodemodReport;
  runtimeUsage?: LopperRuntimeUsage;
  reachabilityConfidence?: LopperReachabilityConfidence;
  removalCandidate?: LopperRemovalCandidate;
  license?: LopperDependencyLicense;
  provenance?: LopperDependencyProvenance;
  vulnerabilities?: LopperVulnerabilityFinding[];
}

export interface LopperVulnerabilityFinding {
  advisoryId: string;
  package: string;
  severity: string;
  fixedVersion?: string;
  source: string;
  priority: LopperVulnerabilityPriority;
  priorityScore: number;
  reachable: boolean;
  evidence?: string[];
}

export interface LopperDependencyLicense {
  spdx?: string;
  raw?: string;
  source?: string;
  confidence?: string;
  unknown?: boolean;
  denied?: boolean;
  evidence?: string[];
}

export interface LopperDependencyProvenance {
  source?: string;
  confidence?: string;
  signals?: string[];
}

export interface LopperRiskCue {
  code: string;
  severity: string;
  message: string;
}

export interface LopperRecommendation {
  code: string;
  priority: string;
  message: string;
}

export interface LopperImportUse {
  name: string;
  module?: string;
  locations?: LopperLocation[];
  provenance?: string[];
  confidenceScore?: number;
  confidenceReasonCodes?: string[];
}

export interface LopperLocation {
  file: string;
  line: number;
  column: number;
}

export interface LopperCodemodReport {
  mode: string;
  suggestions?: LopperCodemodSuggestion[];
  apply?: LopperCodemodApplyReport;
}

export interface LopperCodemodSuggestion {
  language?: string;
  dependency?: string;
  file: string;
  targetFile?: string;
  line: number;
  importName: string;
  fromModule: string;
  toModule: string;
  original: string;
  replacement: string;
  patch: string;
  safetyReasonCodes?: string[];
  deleteLine?: boolean;
}

export interface LopperCodemodApplyReport {
  appliedFiles?: number;
  appliedPatches?: number;
  skippedFiles?: number;
  skippedPatches?: number;
  failedFiles?: number;
  failedPatches?: number;
  backupPath?: string;
  results?: LopperCodemodApplyResult[];
}

export interface LopperCodemodApplyResult {
  file: string;
  status: string;
  patchCount: number;
  message?: string;
}

export interface LopperSymbolUsage {
  name: string;
  module?: string;
  count: number;
}

export interface LopperSymbolRef {
  name: string;
  module?: string;
}

export interface LopperRuntimeUsage {
  loadCount: number;
  correlation?: LopperRuntimeCorrelation;
  runtimeOnly?: boolean;
  modules?: LopperRuntimeModuleUsage[];
  parentModules?: LopperRuntimeModuleUsage[];
  entrypoints?: LopperRuntimeModuleUsage[];
  topSymbols?: LopperRuntimeSymbolUsage[];
}

export interface LopperRuntimeModuleUsage {
  module: string;
  count: number;
}

export interface LopperRuntimeSymbolUsage {
  symbol: string;
  module?: string;
  count: number;
}

export interface LopperReachabilityConfidence {
  model: string;
  score: number;
  summary?: string;
  rationaleCodes?: string[];
  signals?: LopperReachabilitySignal[];
}

export interface LopperReachabilitySignal {
  code: string;
  score: number;
  weight: number;
  contribution: number;
}

export interface LopperRemovalCandidate {
  score: number;
  usage: number;
  impact: number;
  confidence: number;
  weights?: LopperRemovalCandidateWeights;
  rationale?: string[];
}

export interface LopperBaselineComparison {
  baselineKey?: string;
  currentKey?: string;
  summaryDelta: LopperSummaryDelta;
  dependencies?: LopperDependencyDelta[];
  regressions?: LopperDependencyDelta[];
  progressions?: LopperDependencyDelta[];
  runtimeRegressions?: LopperDependencyDelta[];
  runtimeImprovements?: LopperDependencyDelta[];
  added?: LopperDependencyDelta[];
  removed?: LopperDependencyDelta[];
  newDeniedLicenses?: LopperDeniedLicenseDelta[];
  newReachableVulnerabilities?: LopperVulnerabilityDelta[];
  unchangedRows?: number;
}

export interface LopperSummaryDelta {
  dependencyCountDelta: number;
  usedExportsCountDelta: number;
  totalExportsCountDelta: number;
  usedPercentDelta: number;
  wastePercentDelta: number;
  unusedBytesDelta: number;
  knownLicenseCountDelta: number;
  unknownLicenseCountDelta: number;
  deniedLicenseCountDelta: number;
  reachableVulnerabilityCountDelta?: number;
}

export interface LopperDependencyDelta {
  kind: "added" | "removed" | "changed";
  language?: string;
  name: string;
  usedExportsCountDelta: number;
  totalExportsCountDelta: number;
  usedPercentDelta: number;
  estimatedUnusedBytesDelta: number;
  wastePercentDelta: number;
  runtimeDelta?: LopperRuntimeDelta;
  deniedIntroduced?: boolean;
  reachableVulnerabilityCountDelta?: number;
  reachableVulnerabilitiesIntroduced?: boolean;
}

export type LopperRuntimeChangeType =
  | "load-count"
  | "new-runtime-loads"
  | "removed-runtime-loads"
  | "correlation"
  | "runtime-only-regression"
  | "runtime-only-improvement"
  | "modules"
  | "parent-modules"
  | "entrypoints";

export type LopperRuntimeCorrelation = "static-only" | "runtime-only" | "overlap";

export interface LopperRuntimeDelta {
  comparable: boolean;
  baselinePresent: boolean;
  currentPresent: boolean;
  baselineLoadCount?: number;
  currentLoadCount?: number;
  loadCountDelta?: number;
  baselineCorrelation?: LopperRuntimeCorrelation;
  currentCorrelation?: LopperRuntimeCorrelation;
  changeTypes?: LopperRuntimeChangeType[];
  newRuntimeLoads?: boolean;
  removedRuntimeLoads?: boolean;
  runtimeOnlyRegression?: boolean;
  runtimeOnlyImprovement?: boolean;
  modulesAdded?: LopperRuntimeModuleDelta[];
  modulesRemoved?: LopperRuntimeModuleDelta[];
  modulesChanged?: LopperRuntimeModuleDelta[];
  parentModulesAdded?: LopperRuntimeModuleDelta[];
  parentModulesRemoved?: LopperRuntimeModuleDelta[];
  parentModulesChanged?: LopperRuntimeModuleDelta[];
  entrypointsAdded?: LopperRuntimeModuleDelta[];
  entrypointsRemoved?: LopperRuntimeModuleDelta[];
  entrypointsChanged?: LopperRuntimeModuleDelta[];
}

export interface LopperRuntimeModuleDelta {
  module: string;
  baselineCount: number;
  currentCount: number;
  countDelta: number;
}

export interface LopperDeniedLicenseDelta {
  language?: string;
  name: string;
  spdx?: string;
}

export interface LopperVulnerabilityDelta {
  language?: string;
  name: string;
  advisoryId: string;
  package: string;
  severity: string;
  fixedVersion?: string;
  source: string;
  priority: LopperVulnerabilityPriority;
  priorityScore: number;
  evidence?: string[];
}
