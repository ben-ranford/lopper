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
  name: string;
  usedExportsCount: number;
  totalExportsCount: number;
  usedPercent: number;
  riskCues?: LopperRiskCue[];
  recommendations?: LopperRecommendation[];
  usedImports?: LopperImportUse[];
  unusedImports?: LopperImportUse[];
  codemod?: LopperCodemodReport;
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
