package php

import (
	"strconv"
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
	for _, expression := range []string{`{$arr[$type::$$property]}`, `{$arr[class_exists($name)]}`, `{$arr[0]} {$arr[class_exists($name)]}`} {
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

func TestDynamicInterpolationCommentBoundaries(t *testing.T) {
	for _, comment := range []string{"/* } */", "// }\n", "# }\n", "/* { */"} {
		for _, wrap := range []func(string) string{
			func(body string) string { return `<?php echo "` + body + `";` },
			func(body string) string { return "<?php echo `" + body + "`;" },
			func(body string) string { return "<?php $doc = <<<DOC\n" + body + "\nDOC;" },
		} {
			source := wrap("{$arr[" + comment + " class_exists($name)]}")
			if !hasDynamicPatterns([]byte(source), "source.php", false) {
				t.Errorf("dynamic cue after comment missed: %q", source)
			}
			source = wrap("{$arr[/* class_exists($name) */ 0]}")
			if hasDynamicPatterns([]byte(source), "source.php", false) {
				t.Errorf("comment-only dynamic cue detected: %q", source)
			}
		}
	}
}

func TestDynamicInterpolationMalformedFragments(t *testing.T) {
	body := strings.Repeat("{$x ", 10000)
	if next, dynamic := scanDynamicInterpolationAt(body, 0); next != len(body) || dynamic {
		t.Fatalf("malformed expression scan = (%d, %v), want (%d, false)", next, dynamic, len(body))
	}
	for _, source := range []string{
		`<?php echo "` + body + `";`,
		"<?php $doc = <<<DOC\n" + body + "\nDOC;",
	} {
		if hasDynamicPatterns([]byte(source), "source.php", false) {
			t.Fatal("malformed fragments contained no dynamic cue")
		}
	}
}

func TestDynamicInterpolationNestedStrings(t *testing.T) {
	for _, tc := range []struct {
		expression string
		want       bool
	}{
		{`{$arr["{$type::$$property}"]}`, true},
		{"{$arr[`{$type::$$property}`]}", true},
		{`{$arr['{$type::$$property}']}`, false},
		{`{$arr["class_exists($name)"]}`, false},
		{`{$arr["\{$type::$$property}"]}`, false},
		{`{$arr[/* "{$type::$$property}" */ 0]}`, false},
	} {
		for _, marker := range []string{"DOC", `"DOC"`, "'DOC'"} {
			source := "<?php $doc = <<<" + marker + "\n" + tc.expression + "\nDOC;"
			want := tc.want && marker != "'DOC'"
			if got := hasDynamicPatterns([]byte(source), "source.php", false); got != want {
				t.Errorf("source %q: got %v, want %v", source, got, want)
			}
		}
	}
}

func TestDynamicInterpolationConsumesLongVariableTokens(t *testing.T) {
	for _, size := range []int{5000, 10000, 20000} {
		for _, token := range []string{strings.Repeat("$", size) + "x", "$x" + strings.Repeat("->$x", size)} {
			for _, suffix := range []string{"", "::$$property"} {
				if next, dynamic := scanDynamicInterpolationToken(token+suffix, 0); next != len(token) || dynamic != (suffix != "") {
					t.Fatalf("token length %d suffix %q: consumed %d, dynamic %v", len(token), suffix, next, dynamic)
				}
				if got := hasDynamicInterpolationExpression(token + suffix); got != (suffix != "") {
					t.Fatalf("token length %d suffix %q: dynamic %v", len(token), suffix, got)
				}
			}
		}
	}
}

func BenchmarkDynamicInterpolationDollarRun(b *testing.B) {
	for _, size := range []int{5000, 10000, 20000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			source := `"{` + strings.Repeat("$", size) + `x}"`
			b.SetBytes(int64(len(source)))
			for b.Loop() {
				if hasPHPDynamicInterpolation(source) {
					b.Fatal("unexpected dynamic cue")
				}
			}
		})
	}
}
