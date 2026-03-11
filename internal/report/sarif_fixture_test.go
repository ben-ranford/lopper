package report

func sampleSARIFReport() Report {
	wasteIncrease := 2.5
	return Report{
		SchemaVersion: "0.1.0",
		Dependencies: []DependencyReport{
			{
				Language: "js-ts",
				Name:     "lodash",
				UsedImports: []ImportUse{
					{
						Name:   "map",
						Module: "lodash/map",
						Locations: []Location{
							{File: "src/main.ts", Line: 12, Column: 4},
						},
					},
				},
				UnusedImports: []ImportUse{
					{
						Name:   "debounce",
						Module: "lodash",
						Locations: []Location{
							{File: "src/main.ts", Line: 3, Column: 1},
						},
					},
				},
				UnusedExports: []SymbolRef{
					{Name: "omit", Module: "lodash"},
				},
				RiskCues: []RiskCue{
					{Code: "dynamic-loader", Severity: "medium", Message: "dynamic module loading detected"},
				},
				Recommendations: []Recommendation{
					{Code: "prefer-subpath-imports", Priority: "high", Message: "switch to subpath imports"},
				},
			},
		},
		WasteIncreasePercent: &wasteIncrease,
	}
}
