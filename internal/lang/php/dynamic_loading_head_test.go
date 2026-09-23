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
		{`<?php echo "\{$type::$$property}";`, false, true},
		{"<?php $doc = <<<DOC\n\\{$type::$$property}\nDOC;", false, true},
	} {
		if got := hasDynamicPatterns([]byte(tc.source), "source.php", tc.shortTags); got != tc.want {
			t.Errorf("source %q shortTags=%v: got %v, want %v", tc.source, tc.shortTags, got, tc.want)
		}
	}
}

func TestDynamicInterpolationPreservesBackslashBeforeBrace(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		prefix := strings.Repeat(`\`, count)
		for _, expression := range []string{`{$type::$$property}`, `{ literal`} {
			for _, source := range []string{
				`<?php echo "` + prefix + expression + `";`,
				"<?php echo `" + prefix + expression + "`;",
				"<?php $doc = <<<DOC\n" + prefix + expression + "\nDOC;",
			} {
				want := expression == `{$type::$$property}`
				if got := hasDynamicPatterns([]byte(source), "source.php", false); got != want {
					t.Errorf("source %q: got %v, want %v", source, got, want)
				}
			}
		}
	}
}
