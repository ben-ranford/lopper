package golang

import (
	"errors"
	"strings"
	"testing"
)

func TestOversizedRootGoModKeepsLongGodebugDirectivesBeforeModule(t *testing.T) {
	for name, lines := range map[string][]string{
		"line": {
			"godebug default=" + strings.Repeat("x", 70*1024),
			"module example.com/root",
		},
		"block": {
			"godebug (",
			"default=" + strings.Repeat("x", 70*1024),
			")",
			"module example.com/root",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeOversizedRootGoModLines(t, repo, lines...)

			requireOversizedRootModulePath(t, repo, "module path extraction after long godebug directive")
		})
	}
}

func TestOversizedRootGoModRejectsMalformedLongGodebugAndIgnoreDirectives(t *testing.T) {
	for name, lines := range map[string][]string{
		"godebug line": {
			"module example.com/root",
			"godebug default=" + strings.Repeat("x", 70*1024) + " extra",
		},
		"godebug block": {
			"module example.com/root",
			"godebug (",
			"default=" + strings.Repeat("x", 70*1024) + " extra",
			")",
		},
		"ignore line": {
			"module example.com/root",
			"ignore ./" + strings.Repeat("nested/", 10*1024) + "dir extra",
		},
		"ignore block": {
			"module example.com/root",
			"ignore (",
			"./" + strings.Repeat("nested/", 10*1024) + "dir extra",
			")",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeOversizedRootGoModLines(t, repo, lines...)

			requireNoTrustedOversizedRootModuleMetadata(t, repo)
		})
	}
}

func TestGoModModuleScannerReconstructsLongSupportedFallbackDirectives(t *testing.T) {
	longGodebug := "default=" + strings.Repeat("x", 70*1024)
	longIgnore := "./" + strings.Repeat("nested/", 10*1024) + "dir"

	for name, tc := range map[string]struct {
		lines    []string
		wantBody string
	}{
		"godebug line": {
			lines: []string{
				"godebug " + longGodebug,
				"module example.com/root",
			},
			wantBody: "godebug " + longGodebug + "\n",
		},
		"ignore line": {
			lines: []string{
				"ignore " + longIgnore,
				"module example.com/root",
			},
			wantBody: "ignore " + longIgnore + "\n",
		},
		"godebug block": {
			lines: []string{
				"godebug (",
				longGodebug,
				")",
				"module example.com/root",
			},
			wantBody: "godebug (\n" + longGodebug + "\n)\n",
		},
		"ignore block": {
			lines: []string{
				"ignore (",
				longIgnore,
				")",
				"module example.com/root",
			},
			wantBody: "ignore (\n" + longIgnore + "\n)\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			modulePath, body, err := scanRecoveredGoModFallback(tc.lines...)
			if err != nil {
				t.Fatalf("scanGoModModulePathWithParser: %v", err)
			}
			if modulePath != "example.com/root" {
				t.Fatalf("module path = %q, want %q", modulePath, "example.com/root")
			}
			if body != tc.wantBody {
				t.Fatalf("synthetic body mismatch\nwant: %q\ngot:  %q", tc.wantBody, body)
			}
		})
	}
}

func TestGoModModuleScannerDefensiveLongDirectiveBranches(t *testing.T) {
	t.Run("recovered fragment bounds", func(t *testing.T) {
		var quotedByteOverflow goModModuleScanner
		quotedByteOverflow.longQuotedTarget.WriteString(strings.Repeat("x", maxLongQuotedGoModTargetBytes))
		quotedByteOverflow.appendLongQuotedTargetByte('y')
		if !quotedByteOverflow.lineInvalid {
			t.Fatalf("expected quoted target byte overflow to invalidate the line")
		}

		var quotedStringOverflow goModModuleScanner
		quotedStringOverflow.longQuotedTarget.WriteString(strings.Repeat("x", maxLongQuotedGoModTargetBytes-1))
		quotedStringOverflow.appendLongQuotedTargetString("yz")
		if !quotedStringOverflow.lineInvalid {
			t.Fatalf("expected quoted target string overflow to invalidate the line")
		}

		var unquotedStringOverflow goModModuleScanner
		unquotedStringOverflow.longUnquotedLine.WriteString(strings.Repeat("x", maxLongUnquotedGoModLineBytes-1))
		unquotedStringOverflow.appendLongUnquotedLineString("yz")
		if !unquotedStringOverflow.lineInvalid {
			t.Fatalf("expected unquoted recovered line overflow to invalidate the line")
		}
	})

	t.Run("oversized directive rejection", func(t *testing.T) {
		moduleBlock := goModModuleScanner{blockDirective: "module"}
		moduleBlock.consumeTooLargeGoModDirectiveLine("module example.com/root", false, false)
		if !moduleBlock.invalid {
			t.Fatalf("expected oversized module block line to fail closed")
		}

		blank := goModModuleScanner{}
		blank.consumeTooLargeGoModDirectiveLine(" \t\r ", false, false)
		if !blank.invalid {
			t.Fatalf("expected blank oversized directive line to fail closed")
		}
	})

	t.Run("incomplete recovered directives", func(t *testing.T) {
		quoted := goModModuleScanner{}
		if _, ok := quoted.longQuotedGoModLine(`replace example.com/a => "unterminated`); ok {
			t.Fatalf("expected quoted recovery without target state to fail")
		}

		quoted.longQuotedQuote = '"'
		quoted.longQuotedTarget.WriteString("./replacement")
		if _, ok := quoted.longQuotedGoModLine("replace example.com/a => ./replacement"); ok {
			t.Fatalf("expected quoted recovery without an opening quote marker to fail")
		}

		unquoted := goModModuleScanner{longUnquotedStart: len("ignore ./nested")}
		if _, ok := unquoted.longUnquotedGoModLine("ignore ./nested"); ok {
			t.Fatalf("expected unquoted recovery without captured suffix to fail")
		}
	})

	t.Run("synthetic body size limit", func(t *testing.T) {
		var scanner goModModuleScanner
		scanner.syntheticBody.WriteString(strings.Repeat("x", maxGoModModuleScanBytes))
		scanner.appendSyntheticGoModLine("y")
		if !scanner.invalid {
			t.Fatalf("expected synthetic body overflow to invalidate the scanner")
		}
	})
}

func TestGoModModuleScannerAncillaryFallbackBranches(t *testing.T) {
	t.Run("scan limit suppression only hides sentinel", func(t *testing.T) {
		if err := suppressGoModModuleScanLimit(errGoModModuleScanTooLarge); err != nil {
			t.Fatalf("expected size-limit sentinel to be suppressed, got %v", err)
		}

		other := errors.New("other")
		if err := suppressGoModModuleScanLimit(other); !errors.Is(err, other) {
			t.Fatalf("expected non-sentinel error to survive, got %v", err)
		}
	})

	t.Run("oversized go.mod path read errors stay surfaced", func(t *testing.T) {
		if _, err := readOversizedGoModModulePath("\x00", "go.mod"); err == nil {
			t.Fatalf("expected invalid repo path to fail oversized go.mod read")
		}

		repo := t.TempDir()
		if _, err := readOversizedGoModModulePath(repo, repo+"/go.mod"); err == nil {
			t.Fatalf("expected missing oversized go.mod to fail open")
		}
	})

	t.Run("directive classification stays narrow", func(t *testing.T) {
		if !isValidLongGoModDirective("replace", true) {
			t.Fatalf("expected quoted replace to remain valid for long-line recovery")
		}
		if isValidLongGoModDirective("replace", false) {
			t.Fatalf("expected unquoted replace to remain invalid for long-line recovery")
		}
		if isValidLongGoModDirective("unknown", false) {
			t.Fatalf("expected unknown long directive to remain invalid")
		}

		if !isValidGoModBlockLine("", "retract", "v2.0.0") {
			t.Fatalf("expected retract body with content before module path to remain valid")
		}
		if isValidGoModBlockLine("", "retract", "") {
			t.Fatalf("expected empty retract body before module path to fail closed")
		}
		if isValidGoModBlockLine("", "unknown", "value") {
			t.Fatalf("expected unknown block directive lines to remain invalid")
		}
	})

	t.Run("long recovery tracks quotes, block lines, and suffix bounds", func(t *testing.T) {
		var missingQuote goModModuleScanner
		missingQuote.line.WriteString("godebug default=go1.21")
		missingQuote.quoteByte = '"'
		missingQuote.startLongQuotedTarget()
		if !missingQuote.lineInvalid {
			t.Fatalf("expected missing opening quote to invalidate long quoted recovery")
		}

		var suffixOverflow goModModuleScanner
		suffixOverflow.lineTooLarge = true
		suffixOverflow.lineTooLargeInQuote = true
		suffixOverflow.lineQuoteClosed = true
		suffixOverflow.longQuotedSuffix.WriteString(strings.Repeat("x", maxLongQuotedGoModSuffixBytes))
		if err := suffixOverflow.consumeLongQuotedLineSuffix('y'); err != nil {
			t.Fatalf("consumeLongQuotedLineSuffix: %v", err)
		}
		if !suffixOverflow.lineInvalid {
			t.Fatalf("expected oversized long quoted suffix to invalidate the line")
		}

		moduleBlock := goModModuleScanner{blockDirective: "module"}
		moduleBlock.consumeGoModBlockLine("example.com/root")
		if moduleBlock.modulePath != "example.com/root" || moduleBlock.invalid {
			t.Fatalf("expected module block line to set module path, got %#v", moduleBlock)
		}

		invalidBlock := goModModuleScanner{blockDirective: "retract"}
		invalidBlock.consumeGoModBlockLine("")
		if !invalidBlock.invalid {
			t.Fatalf("expected invalid retract block line to fail closed")
		}
	})

	t.Run("long directive acceptance requires complete recovered state", func(t *testing.T) {
		quotedReplacement := goModModuleScanner{
			longQuotedQuote: '"',
			parseSynthetic:  func(string, string) bool { return true },
		}
		quotedReplacement.longQuotedTarget.WriteString("./replacement")
		quotedReplacement.longQuotedSuffix.WriteString(" // keep")
		if !quotedReplacement.acceptLongGoModDirectiveLine("replace", `replace example.com/a => "`, true, true) {
			t.Fatalf("expected complete quoted replace recovery to succeed")
		}

		unterminated := goModModuleScanner{
			longQuotedQuote: '"',
			parseSynthetic:  func(string, string) bool { return true },
		}
		unterminated.longQuotedTarget.WriteString("./replacement")
		if unterminated.acceptLongGoModDirectiveLine("replace", `replace example.com/a => "`, true, false) {
			t.Fatalf("expected unterminated quoted recovery to fail")
		}

		var blockEntry goModModuleScanner
		blockEntry.blockDirective = "godebug"
		blockEntry.line.WriteString("default=go1.21")
		blockEntry.startLongUnquotedLine()
		if blockEntry.invalid || blockEntry.longUnquotedStart != 0 || blockEntry.longUnquotedLine.String() != "default=go1.21" {
			t.Fatalf("expected block long-line recovery to keep the full block entry, got %#v", blockEntry)
		}

		var rootDirective goModModuleScanner
		rootDirective.line.WriteString("godebug")
		rootDirective.startLongUnquotedLine()
		if !rootDirective.lineInvalid {
			t.Fatalf("expected root-level long-line recovery without a split point to fail closed")
		}
	})

	t.Run("quoted string and recovery guards fail closed", func(t *testing.T) {
		rawQuoted := goModModuleScanner{
			inQuotedString: true,
			quoteByte:      '`',
		}
		rawQuoted.consumeQuotedStringByte('\n')
		if !rawQuoted.invalid || rawQuoted.inQuotedString || rawQuoted.quoteByte != 0 {
			t.Fatalf("expected raw-quoted newline to fail closed and exit quoted mode, got %#v", rawQuoted)
		}

		guardedQuoted := goModModuleScanner{lineInvalid: true, quoteByte: '"'}
		guardedQuoted.line.WriteString(`replace example.com/a => "`)
		guardedQuoted.startLongQuotedTarget()
		if guardedQuoted.longQuotedTarget.Len() != 0 {
			t.Fatalf("expected invalid lines to skip long quoted target recovery")
		}

		guardedUnquoted := goModModuleScanner{lineInvalid: true}
		guardedUnquoted.line.WriteString("default=go1.21")
		guardedUnquoted.startLongUnquotedLine()
		if guardedUnquoted.longUnquotedLine.Len() != 0 {
			t.Fatalf("expected invalid lines to skip long unquoted recovery")
		}
	})
}

func scanRecoveredGoModFallback(lines ...string) (string, string, error) {
	var body string
	modulePath, err := scanGoModModulePathWithParser(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(modulePath, recovered string) bool {
		body = recovered
		return true
	})
	return modulePath, body, err
}
