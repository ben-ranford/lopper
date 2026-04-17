export const lopperScopeModeValues = ["package", "repo", "changed-packages"] as const;
export type LopperScopeMode = typeof lopperScopeModeValues[number];

export interface LopperReport {
  summary?: LopperSummary;
  dependencies: LopperDependencyReport[];
  warnings?: string[];
}

export interface LopperSummary {
  dependencyCount: number;
  usedPercent: number;
}

export interface LopperDependencyReport {
  language?: string;
  name: string;
  usedExportsCount: number;
  totalExportsCount: number;
  usedPercent: number;
  riskCues?: LopperRiskCue[];
  recommendations?: LopperRecommendation[];
  usedImports?: LopperImportUse[];
  unusedImports?: LopperImportUse[];
  codemod?: LopperCodemodReport;
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
  locations?: LopperLocation[];
}

export interface LopperLocation {
  file: string;
  line: number;
  column: number;
}

export interface LopperCodemodReport {
  mode: string;
  suggestions?: LopperCodemodSuggestion[];
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
