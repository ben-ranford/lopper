package runtime

type Event struct {
	Language   string `json:"language,omitempty"`
	Dependency string `json:"dependency,omitempty"`
	Module     string `json:"module"`
	Resolved   string `json:"resolved,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Parent     string `json:"parent,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
}

type DependencyKey struct {
	Language string
	Name     string
}

type Trace struct {
	DependencyLoads       map[string]int
	DependencyModules     map[string]map[string]int
	DependencyParents     map[string]map[string]int
	DependencyEntrypoints map[string]map[string]int
	DependencySymbols     map[string]map[string]int

	DependencyLoadsByLanguage       map[DependencyKey]int
	DependencyModulesByLanguage     map[DependencyKey]map[string]int
	DependencyParentsByLanguage     map[DependencyKey]map[string]int
	DependencyEntrypointsByLanguage map[DependencyKey]map[string]int
	DependencySymbolsByLanguage     map[DependencyKey]map[string]int
}

type AnnotateOptions struct {
	IncludeRuntimeOnlyRows bool
	SupportedLanguages     []string
}
