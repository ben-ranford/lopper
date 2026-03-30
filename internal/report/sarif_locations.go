package report

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func dependencyAnchorLocation(dep DependencyReport) *sarifLocation {
	locations := make([]Location, 0)
	for _, imp := range dep.UsedImports {
		locations = append(locations, imp.Locations...)
	}
	for _, imp := range dep.UnusedImports {
		locations = append(locations, imp.Locations...)
	}
	if len(locations) == 0 {
		return nil
	}
	sort.SliceStable(locations, func(i, j int) bool {
		left := filepath.ToSlash(locations[i].File)
		right := filepath.ToSlash(locations[j].File)
		if left != right {
			return left < right
		}
		if locations[i].Line != locations[j].Line {
			return locations[i].Line < locations[j].Line
		}
		return locations[i].Column < locations[j].Column
	})
	loc, ok := toSARIFLocation(locations[0])
	if !ok {
		return nil
	}
	return &loc
}

func toSARIFLocations(locations []Location) []sarifLocation {
	if len(locations) == 0 {
		return nil
	}
	result := make([]sarifLocation, 0, len(locations))
	seen := make(map[string]struct{})
	for _, location := range locations {
		loc, ok := toSARIFLocation(location)
		if !ok {
			continue
		}
		key := sarifLocationKey(loc)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, loc)
	}
	sort.SliceStable(result, func(i, j int) bool {
		li := result[i].PhysicalLocation.ArtifactLocation.URI
		lj := result[j].PhysicalLocation.ArtifactLocation.URI
		if li != lj {
			return li < lj
		}
		regionI := result[i].PhysicalLocation.Region
		regionJ := result[j].PhysicalLocation.Region
		lineI, colI := 0, 0
		lineJ, colJ := 0, 0
		if regionI != nil {
			lineI, colI = regionI.StartLine, regionI.StartColumn
		}
		if regionJ != nil {
			lineJ, colJ = regionJ.StartLine, regionJ.StartColumn
		}
		if lineI != lineJ {
			return lineI < lineJ
		}
		return colI < colJ
	})
	if len(result) == 0 {
		return nil
	}
	return result
}

func sarifLocationKey(location sarifLocation) string {
	line := 0
	column := 0
	if location.PhysicalLocation.Region != nil {
		line = location.PhysicalLocation.Region.StartLine
		column = location.PhysicalLocation.Region.StartColumn
	}
	return location.PhysicalLocation.ArtifactLocation.URI + "\x00" + fmt.Sprintf("%d:%d", line, column)
}

func toSARIFLocation(location Location) (sarifLocation, bool) {
	file := strings.TrimSpace(location.File)
	if file == "" {
		return sarifLocation{}, false
	}
	file = toSARIFArtifactURI(file)
	loc := sarifLocation{
		PhysicalLocation: sarifPhysicalLocation{
			ArtifactLocation: sarifArtifactLocation{URI: file},
		},
	}
	if location.Line > 0 || location.Column > 0 {
		region := &sarifRegion{}
		if location.Line > 0 {
			region.StartLine = location.Line
		}
		if location.Column > 0 {
			region.StartColumn = location.Column
		}
		loc.PhysicalLocation.Region = region
	}
	return loc, true
}

func toSARIFArtifactURI(file string) string {
	file = strings.ReplaceAll(file, "\\", "/")
	file = path.Clean(file)
	if isWindowsDriveAbsolutePath(file) || filepath.IsAbs(file) {
		return fileURLFromPath(file)
	}
	return file
}

func fileURLFromPath(pathValue string) string {
	slashed := strings.ReplaceAll(pathValue, "\\", "/")
	slashed = filepath.ToSlash(slashed)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{
		Scheme: "file",
		Path:   slashed,
	}).String()
}

func isWindowsDriveAbsolutePath(pathValue string) bool {
	if len(pathValue) < 3 {
		return false
	}
	drive := pathValue[0]
	if (drive < 'A' || drive > 'Z') && (drive < 'a' || drive > 'z') {
		return false
	}
	if pathValue[1] != ':' {
		return false
	}
	return pathValue[2] == '/' || pathValue[2] == '\\'
}

func resultLocationKey(result sarifResult) string {
	if len(result.Locations) == 0 {
		return ""
	}
	loc := result.Locations[0]
	line, col := 0, 0
	if loc.PhysicalLocation.Region != nil {
		line = loc.PhysicalLocation.Region.StartLine
		col = loc.PhysicalLocation.Region.StartColumn
	}
	return fmt.Sprintf("%s:%d:%d", loc.PhysicalLocation.ArtifactLocation.URI, line, col)
}
