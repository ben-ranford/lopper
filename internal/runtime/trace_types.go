package runtime

type Event struct {
	Module     string `json:"module"`
	Resolved   string `json:"resolved,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Parent     string `json:"parent,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
}

type Trace struct {
	DependencyLoads       map[string]int
	DependencyModules     map[string]map[string]int
	DependencyParents     map[string]map[string]int
	DependencyEntrypoints map[string]map[string]int
	DependencySymbols     map[string]map[string]int
}

type AnnotateOptions struct {
	IncludeRuntimeOnlyRows bool
}
