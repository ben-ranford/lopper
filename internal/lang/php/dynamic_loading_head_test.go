package php

import (
	"strings"
	"testing"
)

func TestDynamicPatternsRespectTemplateAndInterpolationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		source    string
		shortTags bool
		want      bool
	}{
		{`It's ready <? new $type; ?>`, false, false},
		{`It's ready <? new $type; ?>`, true, true},
		{`<?php echo ` + "`{$type::$$property}`" + `;`, false, true},
		{"<?php $doc = <<<\"DOC\"\n{$type::$$property}\nDOC;", false, true},
		{"<?php $doc = <<<DOC\n{$type::$$property}", false, true},
		{"<?php $doc = <<<DOC\n{\\$type::$$property}\nDOC;", false, false},
		{"<?php $doc = <<<\n", false, false},
		{`<?php /* "{$type::$$property}" */`, false, false},
		{`<?php echo "\{$type::$$property}";`, false, false},
		{"<?php $doc = <<<DOC\n\\{$type::$$property}\nDOC;", false, false},
	} {
		if got := hasDynamicPatterns([]byte(tc.source), "source.php", tc.shortTags); got != tc.want {
			t.Errorf("source %q shortTags=%v: got %v, want %v", tc.source, tc.shortTags, got, tc.want)
		}
	}
}

func TestDynamicInterpolationRespectsBackslashParity(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3, 4} {
		prefix := strings.Repeat(`\`, count)
		for _, expression := range []string{`{$type::$$property}`, `{ literal`} {
			for _, source := range []string{
				`<?php echo "` + prefix + expression + `";`,
				"<?php echo `" + prefix + expression + "`;",
				"<?php $doc = <<<DOC\n" + prefix + expression + "\nDOC;",
			} {
				want := expression == `{$type::$$property}` && count%2 == 0
				if got := hasDynamicPatterns([]byte(source), "source.php", false); got != want {
					t.Errorf("source %q: got %v, want %v", source, got, want)
				}
			}
		}
	}
}

func TestDynamicInterpolationIndirectVariables(t *testing.T) {
	for _, expression := range []string{`{$$type::$property}`, `{$obj->$type::$property}`, `{$obj->child->$type::$property}`} {
		for _, source := range []string{
			`<?php echo "` + expression + `";`,
			"<?php echo `" + expression + "`;",
			"<?php $doc = <<<DOC\n" + expression + "\nDOC;",
		} {
			if !hasDynamicPatterns([]byte(source), "source.php", false) {
				t.Errorf("executable indirect static interpolation missed: %q", source)
			}
		}
		for _, source := range []string{
			`<?php echo '` + expression + `';`,
			"<?php $doc = <<<'DOC'\n" + expression + "\nDOC;",
			`<?php /* "` + expression + `" */`,
			`<?php echo "\` + expression + `";`,
		} {
			if hasDynamicPatterns([]byte(source), "source.php", false) {
				t.Errorf("non-executable indirect static interpolation detected: %q", source)
			}
		}
	}
}

func TestDynamicInterpolationNestedExpressions(t *testing.T) {
	for _, expression := range []string{`{$arr[$type::$$property]}`, `{$arr[class_exists($name)]}`} {
		for _, source := range []string{
			`<?php echo "` + expression + `";`,
			"<?php echo `" + expression + "`;",
			"<?php $doc = <<<DOC\n" + expression + "\nDOC;",
		} {
			if !hasDynamicPatterns([]byte(source), "source.php", false) {
				t.Errorf("executable nested interpolation missed: %q", source)
			}
		}
	}
	for _, source := range []string{
		`<?php echo "{$arr['class_exists($name)']}";`,
		`<?php echo "{$arr['$type::$$property']}";`,
		`<?php /* "{$arr[$type::$$property]}" */`,
	} {
		if hasDynamicPatterns([]byte(source), "source.php", false) {
			t.Errorf("literal nested interpolation detected: %q", source)
		}
	}
}
