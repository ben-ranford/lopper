package runtime

type Event struct {
	Module   string `json:"module"`
	Resolved string `json:"resolved,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

type Trace struct {
	DependencyLoads   map[string]int
	DependencyModules map[string]map[string]int
	DependencySymbols map[string]map[string]int
}

type AnnotateOptions struct {
	IncludeRuntimeOnlyRows bool
}
