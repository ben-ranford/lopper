package reusecheck

import "testing"

func TestNestedCollectionIndexes(t *testing.T) {
	for _, collection := range []string{"[][]s.DependencyStats", "[2][3]s.DependencyStats", "*[2][3]s.DependencyStats", "[]*[3]s.DependencyStats", "Outer"} {
		declaration := "type Inner []s.DependencyStats; type Outer []Inner"
		for _, use := range []collectionProvenanceUse{
			{"matrix " + collection, "measured := matrix[0][0]", "measured", false},
			{"matrix " + collection, "", "matrix[0][0]", false},
			{"matrix " + collection, "row := matrix[0]; measured := row[0]", "measured", false},
			{"matrix " + collection, "for _, measured := range matrix[0] {", "measured", true},
			{"matrix " + collection, "for _, row := range matrix { measured := row[0]", "measured", true},
		} {
			t.Run(collection+use.setup+use.receiver, func(t *testing.T) {
				checkCollectionProvenance(t, declaration, use, true)
			})
		}
	}
}

func TestNestedMapCollections(t *testing.T) {
	for _, use := range []collectionProvenanceUse{
		{"matrix map[string][]s.DependencyStats", `measured := matrix["row"][0]`, "measured", false},
		{"matrix map[string][]s.DependencyStats", `row, ok := matrix["row"]; _ = ok`, "row[0]", false},
		{"matrix map[string][]s.DependencyStats", `row, ok := matrix["row"]; _ = ok; for _, measured := range row {`, "measured", true},
		{"matrix map[string][]s.DependencyStats", `for _, row := range matrix { measured := row[0]`, "measured", true},
		{"matrix map[*[2]s.DependencyStats]int", `for row := range matrix { measured := row[0]`, "measured", true},
		{"matrix map[string]map[string]s.DependencyStats", `measured, ok := matrix["row"]["item"]; _ = ok`, "measured", false},
		{"matrix []map[string]s.DependencyStats", "", `matrix[0]["item"]`, false},
	} {
		t.Run(use.parameter+use.setup, func(t *testing.T) {
			checkCollectionProvenance(t, "", use, true)
		})
	}
}

func TestNestedCollectionOperations(t *testing.T) {
	for _, setup := range []string{
		"measured := matrix[0][:][0]",
		"row := append(matrix[0], s.DependencyStats{}); measured := row[0]",
		"row := &matrix[0]; measured := (*row)[0]",
	} {
		checkCollectionProvenance(t, "", collectionProvenanceUse{"matrix [][]s.DependencyStats", setup, "measured", false}, true)
	}
	checkCollectionProvenance(t, "", collectionProvenanceUse{"matrix [][]*s.DependencyStats", "", "(*matrix[0][0])", false}, true)
	checkCollectionProvenance(t, "func factory() [][]s.DependencyStats { panic(0) }", collectionProvenanceUse{"unused string", "measured := factory()[0][0]", "measured", false}, true)
	checkCollectionProvenance(t, "", collectionProvenanceUse{"raw any", "", "raw.([][]s.DependencyStats)[0][0]", false}, true)
}

func TestNestedCollectionIndexGuards(t *testing.T) {
	for _, use := range []collectionProvenanceUse{
		{"matrix [][]s.DependencyStats", "row, ok := matrix[0]; _ = ok", "row[0]", false},
		{"matrix [2][]s.DependencyStats", "row, ok := matrix[0]; _ = ok; for _, measured := range row {", "measured", true},
		{"matrix map[string][]s.DependencyStats", `_, row := matrix["row"]`, "row[0]", false},
		{"matrix map[string][]s.DependencyStats", "for row := range matrix { measured := row[0]", "measured", true},
		{"matrix map[*[2]s.DependencyStats]int", "for _, row := range matrix { measured := row[0]", "measured", true},
		{"matrix [][]Stats", "measured := matrix[0][0]", "measured", false},
		{"matrix *[][]s.DependencyStats", "", "matrix[0][0]", false},
		{"matrix chan []s.DependencyStats", "measured := matrix[0][0]", "measured", false},
	} {
		t.Run(use.parameter+use.setup, func(t *testing.T) {
			checkCollectionProvenance(t, "type Stats s.DependencyStats", use, false)
		})
	}
	for _, declaration := range []string{
		"var matrix = matrix[0]",
		"var matrix = other[0]; var other = matrix[0]",
		"var matrix, ok = matrix[0]",
		"var matrix, ok = other[0]; var other = matrix[0]",
	} {
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"unused string", "measured := matrix[0][0]", "measured", false}, false)
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"unused string", "for _, row := range matrix { measured := row[0]", "measured", true}, false)
	}
}
