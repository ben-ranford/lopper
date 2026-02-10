package analysis

type Request struct {
	RepoPath   string
	Dependency string
	TopN       int
	Language   string
}
