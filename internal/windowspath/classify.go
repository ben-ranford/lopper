package windowspath

import (
	pathpkg "path"
	"strings"
)

type Kind uint8

const (
	KindNone Kind = iota
	KindAmbiguous
	KindDriveAbsolute
	KindDriveRelative
	KindRootedWithoutDrive
	KindUNCAbsolute
	KindUNCIncomplete
)

type Classification struct {
	Kind   Kind
	Volume string
	Path   string
}

func (c *Classification) IsAbsolute() bool {
	return c.Kind == KindDriveAbsolute || c.Kind == KindUNCAbsolute
}

func Classify(value string) Classification {
	if len(value) == 0 {
		return Classification{}
	}
	if info, ok := classifyNamespacePath(value); ok {
		return info
	}
	if len(value) >= 2 && isPathSeparator(value[0]) && isPathSeparator(value[1]) {
		return classifyUNCPath(value)
	}
	if len(value) >= 2 && isASCIIAlpha(value[0]) && value[1] == ':' {
		if len(value) >= 3 && isPathSeparator(value[2]) {
			return Classification{
				Kind:   KindDriveAbsolute,
				Volume: value[:2],
				Path:   value[3:],
			}
		}
		return Classification{
			Kind:   KindDriveRelative,
			Volume: value[:2],
			Path:   value[2:],
		}
	}
	if value[0] == '\\' {
		return Classification{
			Kind: KindRootedWithoutDrive,
			Path: value[1:],
		}
	}
	return Classification{}
}

func Clean(value string) string {
	slashed := strings.ReplaceAll(value, `\`, "/")
	return pathpkg.Clean("/" + slashed)
}

func HasReservedDOSNameComponent(value string) bool {
	return hasWindowsPathComponent(value, IsReservedDOSName)
}

func HasTrimmedComponentAlias(value string) bool {
	return hasWindowsPathComponent(value, func(component string) bool {
		return component != strings.TrimRight(component, " .")
	})
}

func IsReservedDOSName(component string) bool {
	trimmed := strings.TrimRight(component, " .")
	if trimmed == "" {
		return false
	}
	if dot := strings.IndexByte(trimmed, '.'); dot >= 0 {
		trimmed = trimmed[:dot]
	}
	trimmed = strings.TrimRight(trimmed, " .")
	if trimmed == "" {
		return false
	}
	switch strings.ToUpper(trimmed) {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func classifyNamespacePath(value string) (Classification, bool) {
	switch {
	case strings.HasPrefix(value, `\\?\`),
		strings.HasPrefix(value, `\\.\`),
		strings.HasPrefix(value, `\??\`),
		strings.HasPrefix(value, `\\??\`),
		strings.HasPrefix(value, `\\?\GLOBALROOT`),
		strings.HasPrefix(value, `\\.\GLOBALROOT`),
		strings.HasPrefix(value, `\GLOBALROOT\`),
		strings.HasPrefix(value, `\Device\`),
		strings.HasPrefix(value, `\\.\pipe\`),
		strings.HasPrefix(value, `\\.\pipe/`):
		return Classification{Kind: KindAmbiguous}, true
	default:
		return Classification{}, false
	}
}

func classifyUNCPath(value string) Classification {
	parts := strings.FieldsFunc(value[2:], func(r rune) bool {
		return r == '\\' || r == '/'
	})
	if len(parts) < 2 {
		return Classification{Kind: KindUNCIncomplete}
	}
	info := Classification{
		Kind:   KindUNCAbsolute,
		Volume: `\\` + parts[0] + `\` + parts[1],
	}
	if len(parts) > 2 {
		info.Path = strings.Join(parts[2:], `\`)
	}
	return info
}

func isPathSeparator(b byte) bool {
	return b == '\\' || b == '/'
}

func isASCIIAlpha(b byte) bool {
	return ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}

func splitWindowsPathComponents(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == '\\' || r == '/'
	})
}

func hasWindowsPathComponent(value string, predicate func(string) bool) bool {
	info := Classify(value)
	switch info.Kind {
	case KindDriveAbsolute, KindDriveRelative, KindRootedWithoutDrive, KindUNCAbsolute:
		for _, component := range splitWindowsPathComponents(info.Path) {
			if predicate(component) {
				return true
			}
		}
	}
	return false
}
