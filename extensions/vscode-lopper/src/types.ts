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
}

export interface LopperEffectivePolicy {
  sources?: string[];
  thresholds: LopperEffectiveThresholds;
  removalCandidateWeights: LopperRemovalCandidateWeights;
  license: LopperLicensePolicy;
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
  file: string;
  line: number;
  importName: string;
  fromModule: string;
  toModule: string;
  original: string;
  replacement: string;
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
  status: "applied" | "skipped" | "failed" | string;
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
  correlation?: "static-only" | "runtime-only" | "overlap";
  runtimeOnly?: boolean;
  modules?: LopperRuntimeModuleUsage[];
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
  added?: LopperDependencyDelta[];
  removed?: LopperDependencyDelta[];
  newDeniedLicenses?: LopperDeniedLicenseDelta[];
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
  deniedIntroduced?: boolean;
}

export interface LopperDeniedLicenseDelta {
  language?: string;
  name: string;
  spdx?: string;
}
