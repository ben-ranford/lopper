package version

import (
	"fmt"
	"strings"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

type Info struct {
	Version   string
	Commit    string
	BuildDate string
}

func Current() Info {
	return Info{
		Version:   normalizeVersion(version),
		Commit:    normalizeField(commit),
		BuildDate: normalizeField(buildDate),
	}
}

func String() string {
	info := Current()
	return info.String()
}

func (i *Info) String() string {
	base := fmt.Sprintf("lopper %s", i.Version)
	extras := make([]string, 0, 2)
	if i.Commit != "" {
		extras = append(extras, "commit "+i.Commit)
	}
	if i.BuildDate != "" {
		extras = append(extras, "built "+i.BuildDate)
	}
	if len(extras) == 0 {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, strings.Join(extras, ", "))
}

func normalizeVersion(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "dev"
	}
	if strings.HasPrefix(trimmed, "v") && len(trimmed) > 1 {
		return trimmed[1:]
	}
	return trimmed
}

func normalizeField(value string) string {
	trimmed := strings.TrimSpace(value)
	switch trimmed {
	case "", "unknown":
		return ""
	default:
		return trimmed
	}
}
