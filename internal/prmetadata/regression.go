package prmetadata

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	fixTitlePattern             = regexp.MustCompile(`^fix(?:\([a-z0-9][a-z0-9._/-]*\))?!?:`)
	regressionDeclarationLineRE = regexp.MustCompile(`^Regression-Test:\s*(.+?)\s*$`)
	regressionDeclarationRE     = regexp.MustCompile(`^(\./[A-Za-z0-9._/-]+)::(Test[A-Za-z0-9_]+)$`)
	regressionExemptionLineRE   = regexp.MustCompile(`^Regression-Test-Exemption:\s*(.+?)\s*$`)
)

type RegressionDeclaration struct {
	PackagePath string
	TestName    string
}

type RegressionMetadata struct {
	Declarations    []RegressionDeclaration
	ExemptionReason string
}

func IsFixTitle(title string) bool {
	return fixTitlePattern.MatchString(strings.TrimSpace(title))
}

// ParseRegressionExemptionLabel accepts only the exact boolean values emitted by GitHub expressions.
func ParseRegressionExemptionLabel(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("regression exemption label value must be exactly true or false")
	}
}

func ParseRegressionProof(body string) (RegressionMetadata, error) {
	var metadata RegressionMetadata
	seen := make(map[RegressionDeclaration]struct{})

	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		if match := regressionDeclarationLineRE.FindStringSubmatch(line); len(match) == 2 {
			declaration, err := parseRegressionDeclaration(match[1])
			if err != nil {
				return RegressionMetadata{}, err
			}
			if _, ok := seen[declaration]; ok {
				return RegressionMetadata{}, fmt.Errorf("duplicate regression-test declaration %q", declaration.PackagePath+"::"+declaration.TestName)
			}
			seen[declaration] = struct{}{}
			metadata.Declarations = append(metadata.Declarations, declaration)
			continue
		}
		if strings.HasPrefix(line, "Regression-Test:") {
			return RegressionMetadata{}, fmt.Errorf("invalid Regression-Test declaration %q", line)
		}

		if match := regressionExemptionLineRE.FindStringSubmatch(line); len(match) == 2 {
			if metadata.ExemptionReason != "" {
				return RegressionMetadata{}, fmt.Errorf("regression-test-exemption must be declared at most once")
			}
			metadata.ExemptionReason = strings.TrimSpace(match[1])
			continue
		}
		if strings.HasPrefix(line, "Regression-Test-Exemption:") {
			return RegressionMetadata{}, fmt.Errorf("regression-test-exemption must include a non-empty reason")
		}
	}

	if metadata.ExemptionReason != "" && len(metadata.Declarations) > 0 {
		return RegressionMetadata{}, fmt.Errorf("regression-test declarations cannot be combined with regression-test-exemption")
	}

	return metadata, nil
}

func ValidateRegressionRequirements(title, body string, hasExemptionLabel bool) error {
	metadata, err := ParseRegressionProof(body)
	if err != nil {
		return err
	}

	isFix := IsFixTitle(title)
	if !isFix {
		if len(metadata.Declarations) > 0 || metadata.ExemptionReason != "" {
			return fmt.Errorf("regression-test metadata is only allowed on conventional fix(...) pull requests")
		}
		return nil
	}

	if metadata.ExemptionReason != "" {
		if !hasExemptionLabel {
			return fmt.Errorf("regression-test exemption requires the maintainer-controlled regression-exempt label")
		}
		return nil
	}
	if len(metadata.Declarations) == 0 {
		if hasExemptionLabel {
			return fmt.Errorf("the regression-exempt label requires a non-empty Regression-Test-Exemption reason")
		}
		return fmt.Errorf("fix(...) pull requests must declare at least one Regression-Test or one Regression-Test-Exemption")
	}
	return nil
}

func parseRegressionDeclaration(value string) (RegressionDeclaration, error) {
	match := regressionDeclarationRE.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 3 {
		return RegressionDeclaration{}, fmt.Errorf("invalid Regression-Test declaration %q", value)
	}

	packagePath := match[1]
	cleaned := path.Clean(strings.TrimPrefix(packagePath, "./"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return RegressionDeclaration{}, fmt.Errorf("regression-test package path must stay under the repository root")
	}
	canonical := "./" + cleaned
	if canonical != packagePath {
		return RegressionDeclaration{}, fmt.Errorf("regression-test package path must be canonical: %q", packagePath)
	}

	return RegressionDeclaration{
		PackagePath: canonical,
		TestName:    match[2],
	}, nil
}
