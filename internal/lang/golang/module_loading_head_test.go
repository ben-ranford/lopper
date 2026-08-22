package golang

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

func TestGoModModuleScannerReconstructsLongSupportedFallbackDirectives(t *testing.T) {
	longGodebug := "default=" + strings.Repeat("x", 70*1024)
	longIgnore := "./" + strings.Repeat("nested/", 10*1024) + "dir"
	longVersionedReplace := "example.com/" + strings.Repeat("x", 70*1024) + " v1.2.3"
	longToolchain := "go1." + strings.Repeat("0", 70*1024)
	longRetract := "v2." + strings.Repeat("0", 70*1024)
	longGoDirective := "1.23." + strings.Repeat("0", 70*1024)
	longQuotedOldPath := `replace "example.com/` + strings.Repeat("x", 70*1024) + `" => "./a//b"`
	longModulePath := "example.com/" + strings.TrimSuffix(strings.Repeat("service/", 9*1024), "/")
	longQuotedReplacementSuffix := `replace "example.com/` + strings.Repeat("x", 70*1024) + `" => "./` + strings.Repeat("y", 2*1024) + `"`
	longUnquotedOldPathWithQuotedReplacement := `replace example.com/` + strings.Repeat("x", 70*1024) + ` => "./a//b"`

	for name, tc := range map[string]struct {
		lines    []string
		wantPath string
		wantBody string
	}{
		"godebug line": {
			lines: []string{
				"godebug " + longGodebug,
				"module example.com/root",
			},
			wantPath: "example.com/root",
			wantBody: "godebug " + longGodebug + "\n",
		},
		"ignore line": {
			lines: []string{
				"ignore " + longIgnore,
				"module example.com/root",
			},
			wantPath: "example.com/root",
			wantBody: "ignore " + longIgnore + "\n",
		},
		"godebug block": {
			lines: []string{
				"godebug (",
				longGodebug,
				")",
				"module example.com/root",
			},
			wantPath: "example.com/root",
			wantBody: "godebug (\n" + longGodebug + "\n)\n",
		},
		"ignore block": {
			lines: []string{
				"ignore (",
				longIgnore,
				")",
				"module example.com/root",
			},
			wantPath: "example.com/root",
			wantBody: "ignore (\n" + longIgnore + "\n)\n",
		},
		"replace unquoted versioned target": {
			lines: []string{
				"module example.com/root",
				"replace example.com/a => " + longVersionedReplace,
			},
			wantPath: "example.com/root",
			wantBody: "replace example.com/a => " + longVersionedReplace + "\n",
		},
		"replace quoted old path with quoted comment markers in suffix": {
			lines: []string{
				"module example.com/root",
				longQuotedOldPath,
			},
			wantPath: "example.com/root",
			wantBody: longQuotedOldPath + "\n",
		},
		"replace quoted old path with long replacement suffix": {
			lines: []string{
				"module example.com/root",
				longQuotedReplacementSuffix,
			},
			wantPath: "example.com/root",
			wantBody: longQuotedReplacementSuffix + "\n",
		},
		"replace unquoted old path before quoted replacement": {
			lines: []string{
				"module example.com/root",
				longUnquotedOldPathWithQuotedReplacement,
			},
			wantPath: "example.com/root",
			wantBody: longUnquotedOldPathWithQuotedReplacement + "\n",
		},
		"module line": {
			lines: []string{
				"module " + longModulePath,
			},
			wantPath: longModulePath,
		},
		"go line": {
			lines: []string{
				"go " + longGoDirective,
				"module example.com/root",
			},
			wantPath: "example.com/root",
			wantBody: "go " + longGoDirective + "\n",
		},
		"toolchain line": {
			lines: []string{
				"toolchain " + longToolchain,
				"module example.com/root",
			},
			wantPath: "example.com/root",
			wantBody: "toolchain " + longToolchain + "\n",
		},
		"retract line": {
			lines: []string{
				"retract " + longRetract,
				"module example.com/root/v2",
			},
			wantPath: "example.com/root/v2",
			wantBody: "retract " + longRetract + "\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			modulePath, body, err := scanRecoveredGoModFallback(tc.lines...)
			if err != nil {
				t.Fatalf("scanGoModModulePathWithParser: %v", err)
			}
			if modulePath != tc.wantPath {
				t.Fatalf("module path = %q, want %q", modulePath, tc.wantPath)
			}
			if body != tc.wantBody {
				t.Fatalf("synthetic body mismatch\nwant: %q\ngot:  %q", tc.wantBody, body)
			}
		})
	}
}

func TestGoModModuleScannerDefensiveLongDirectiveBranches(t *testing.T) {
	t.Run("recovered fragment bounds", func(t *testing.T) {
		requireLongRecoveredFragmentBounds(t)
	})

	t.Run("oversized directive rejection", func(t *testing.T) {
		requireOversizedDirectiveRejection(t)
	})

	t.Run("incomplete recovered directives", func(t *testing.T) {
		requireIncompleteRecoveredDirectives(t)
	})

	t.Run("synthetic body size limit", func(t *testing.T) {
		requireSyntheticBodySizeLimit(t)
	})
}

func TestGoModModuleScannerAncillaryFallbackBranches(t *testing.T) {
	t.Run("scan limit suppression only hides sentinel", func(t *testing.T) {
		requireScanLimitSuppression(t)
	})

	t.Run("oversized go.mod path read errors stay surfaced", func(t *testing.T) {
		requireOversizedGoModPathReadErrors(t)
	})

	t.Run("directive classification stays narrow", func(t *testing.T) {
		requireDirectiveClassificationStaysNarrow(t)
	})

	t.Run("long recovery tracks quotes, block lines, and suffix bounds", func(t *testing.T) {
		requireLongRecoveryTracksQuotesAndBounds(t)
	})

	t.Run("long directive acceptance requires complete recovered state", func(t *testing.T) {
		requireLongDirectiveAcceptanceNeedsCompleteRecoveredState(t)
	})

	t.Run("quoted string and recovery guards fail closed", func(t *testing.T) {
		requireQuotedStringAndRecoveryGuardsFailClosed(t)
	})
}

func requireLongRecoveredFragmentBounds(t *testing.T) {
	t.Helper()

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
}

func requireOversizedDirectiveRejection(t *testing.T) {
	t.Helper()

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

	blockFailure := goModModuleScanner{blockDirective: "unknown"}
	blockFailure.consumeTooLargeGoModDirectiveLine("value", false, false)
	if !blockFailure.invalid {
		t.Fatalf("expected unknown oversized block directive line to fail closed")
	}

	topLevelFailure := goModModuleScanner{}
	topLevelFailure.consumeTooLargeGoModDirectiveLine("replace example.com/a =>", false, false)
	if !topLevelFailure.invalid {
		t.Fatalf("expected incomplete oversized top-level directive line to fail closed")
	}
}

func requireIncompleteRecoveredDirectives(t *testing.T) {
	t.Helper()

	quoted := goModModuleScanner{}
	if _, ok := quoted.longQuotedGoModLine(`replace example.com/a => "unterminated`); ok {
		t.Fatalf("expected quoted recovery without target state to fail")
	}

	quoted.longQuotedQuote = '"'
	quoted.longQuotedTarget.WriteString("./replacement")
	if _, ok := quoted.longQuotedGoModLine("replace example.com/a => ./replacement"); ok {
		t.Fatalf("expected quoted recovery without an opening quote marker to fail")
	}

	mismatchedQuote := goModModuleScanner{
		longQuotedQuote: '"',
		quoteStartValid: true,
		quoteStart:      0,
	}
	mismatchedQuote.longQuotedTarget.WriteString("./replacement")
	if _, ok := mismatchedQuote.longQuotedGoModLine("`"); ok {
		t.Fatalf("expected quoted recovery with mismatched opening quote to fail")
	}

	emptyQuotedTarget := goModModuleScanner{
		longQuotedQuote: '"',
		quoteStartValid: true,
		quoteStart:      0,
	}
	if _, ok := emptyQuotedTarget.longQuotedGoModLine(`"`); ok {
		t.Fatalf("expected quoted recovery without target content to fail")
	}

	unquoted := goModModuleScanner{longUnquotedStart: len("ignore ./nested")}
	if _, ok := unquoted.longUnquotedGoModLine("ignore ./nested"); ok {
		t.Fatalf("expected unquoted recovery without captured suffix to fail")
	}

	quotedAtOverflowBoundary := goModModuleScanner{
		longQuotedQuote: '"',
		quoteStartValid: true,
		quoteStart:      len(`replace example.com/a => `),
	}
	quotedAtOverflowBoundary.longQuotedTarget.WriteString("./replacement")
	if got, ok := quotedAtOverflowBoundary.longQuotedGoModLine(`replace example.com/a => `); !ok || got != `replace example.com/a => "./replacement"` {
		t.Fatalf("expected quote-start boundary recovery to rebuild line, got %q ok=%v", got, ok)
	}

	unknownDirective := goModModuleScanner{}
	if unknownDirective.acceptLongGoModDirectiveLine("unknown", "unknown value", false, false) {
		t.Fatalf("expected unknown long directive recovery to fail")
	}

	badQuotedRecovery := goModModuleScanner{
		longQuotedQuote: '"',
		quoteStartValid: true,
		quoteStart:      0,
	}
	if badQuotedRecovery.acceptLongGoModDirectiveLine("replace", `replace example.com/a => "`, true, true) {
		t.Fatalf("expected quoted recovery without captured target to fail")
	}

	badUnquotedRecovery := goModModuleScanner{longUnquotedStart: len("require example.com/dep v1.2.3") + 1}
	if badUnquotedRecovery.acceptLongGoModDirectiveLine("require", "require example.com/dep v1.2.3", false, false) {
		t.Fatalf("expected unquoted recovery without bounded suffix to fail")
	}
}

func requireSyntheticBodySizeLimit(t *testing.T) {
	t.Helper()

	var scanner goModModuleScanner
	scanner.syntheticBody.WriteString(strings.Repeat("x", maxGoModModuleScanBytes))
	scanner.appendSyntheticGoModLine("y")
	if !scanner.invalid {
		t.Fatalf("expected synthetic body overflow to invalidate the scanner")
	}
}

func requireScanLimitSuppression(t *testing.T) {
	t.Helper()

	if err := suppressGoModModuleScanLimit(errGoModModuleScanTooLarge); err != nil {
		t.Fatalf("expected size-limit sentinel to be suppressed, got %v", err)
	}

	other := errors.New("other")
	if err := suppressGoModModuleScanLimit(other); !errors.Is(err, other) {
		t.Fatalf("expected non-sentinel error to survive, got %v", err)
	}
}

func requireOversizedGoModPathReadErrors(t *testing.T) {
	t.Helper()

	if _, err := readOversizedGoModModulePath("\x00", "go.mod"); err == nil {
		t.Fatalf("expected invalid repo path to fail oversized go.mod read")
	}

	repo := t.TempDir()
	if _, err := readOversizedGoModModulePath(repo, repo+"/go.mod"); err == nil {
		t.Fatalf("expected missing oversized go.mod to fail open")
	}

	successRepo := t.TempDir()
	writeOversizedRootGoModLines(t, successRepo, "module example.com/root")
	modulePath, err := readOversizedGoModModulePath(successRepo, successRepo+"/go.mod")
	if err != nil {
		t.Fatalf("expected oversized go.mod read to succeed, got %v", err)
	}
	if modulePath != "example.com/root" {
		t.Fatalf("expected oversized go.mod read to recover module path, got %q", modulePath)
	}
}

func requireDirectiveClassificationStaysNarrow(t *testing.T) {
	t.Helper()

	for _, directive := range []string{"go", "toolchain", "replace", "retract"} {
		if !isValidLongGoModDirective(directive) {
			t.Fatalf("expected %q to remain valid for long-line recovery", directive)
		}
	}
	if isValidLongGoModDirective("unknown") {
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
}

func requireLongRecoveryTracksQuotesAndBounds(t *testing.T) {
	t.Helper()
	requireLongRecoveryRejectsBadQuoteState(t)
	requireLongQuotedSuffixBounds(t)
	requireLongQuotedSuffixTracksQuotes(t)
	requireLongRecoveryBlockLineGuards(t)
}

func requireLongRecoveryRejectsBadQuoteState(t *testing.T) {
	t.Helper()
	var missingQuote goModModuleScanner
	missingQuote.line.WriteString("godebug default=go1.21")
	missingQuote.quoteByte = '"'
	missingQuote.quoteStart = len("godebug default=go1.2")
	missingQuote.quoteStartValid = true
	missingQuote.startLongQuotedTarget()
	if !missingQuote.lineInvalid {
		t.Fatalf("expected missing opening quote to invalidate long quoted recovery")
	}

	var openingQuoteOverflow goModModuleScanner
	openingQuoteOverflow.line.WriteString(`replace example.com/a => `)
	openingQuoteOverflow.quoteByte = '"'
	openingQuoteOverflow.quoteStart = openingQuoteOverflow.line.Len()
	openingQuoteOverflow.quoteStartValid = true
	openingQuoteOverflow.startLongQuotedTarget()
	if openingQuoteOverflow.lineInvalid || openingQuoteOverflow.longQuotedTarget.Len() != 0 {
		t.Fatalf("expected opening-quote overflow recovery to stay valid, got %#v", openingQuoteOverflow)
	}

	var missingQuoteStart goModModuleScanner
	missingQuoteStart.quoteByte = '"'
	missingQuoteStart.startLongQuotedTarget()
	if !missingQuoteStart.lineInvalid {
		t.Fatalf("expected missing quote-start metadata to invalidate long quoted recovery")
	}

	var outOfBoundsQuoteStart goModModuleScanner
	outOfBoundsQuoteStart.line.WriteString(`replace example.com/a => "`)
	outOfBoundsQuoteStart.quoteByte = '"'
	outOfBoundsQuoteStart.quoteStart = outOfBoundsQuoteStart.line.Len() + 1
	outOfBoundsQuoteStart.quoteStartValid = true
	outOfBoundsQuoteStart.startLongQuotedTarget()
	if !outOfBoundsQuoteStart.lineInvalid {
		t.Fatalf("expected out-of-bounds quote start to invalidate long quoted recovery")
	}
}

func requireLongQuotedSuffixBounds(t *testing.T) {
	t.Helper()
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
}

func requireLongQuotedSuffixTracksQuotes(t *testing.T) {
	t.Helper()
	requireLongDoubleQuotedSuffixTracksComments(t)
	requireLongRawQuotedSuffixTracksComments(t)
	requireLongEscapedQuotedSuffix(t)
}

func requireLongDoubleQuotedSuffixTracksComments(t *testing.T) {
	t.Helper()
	var quotedSuffix goModModuleScanner
	quotedSuffix.lineTooLarge = true
	quotedSuffix.lineTooLargeInQuote = true
	quotedSuffix.lineQuoteClosed = true
	if err := quotedSuffix.consumeLongQuotedLineSuffix('"'); err != nil {
		t.Fatalf("consumeLongQuotedLineSuffix start quote: %v", err)
	}
	for _, b := range []byte("./a//b\"") {
		if err := quotedSuffix.consumeLongQuotedLineSuffix(b); err != nil {
			t.Fatalf("consumeLongQuotedLineSuffix quoted suffix: %v", err)
		}
	}
	if quotedSuffix.suffixInQuoted {
		t.Fatalf("expected suffix quote tracking to exit quoted mode")
	}
	if got := quotedSuffix.longQuotedSuffix.String(); got != "\"./a//b\"" {
		t.Fatalf("expected quoted suffix to keep comment markers literal, got %q", got)
	}
}

func requireLongRawQuotedSuffixTracksComments(t *testing.T) {
	t.Helper()
	var rawQuotedSuffix goModModuleScanner
	rawQuotedSuffix.lineTooLarge = true
	rawQuotedSuffix.lineTooLargeInQuote = true
	rawQuotedSuffix.lineQuoteClosed = true
	if err := rawQuotedSuffix.consumeLongQuotedLineSuffix('`'); err != nil {
		t.Fatalf("consumeLongQuotedLineSuffix raw quote: %v", err)
	}
	for _, b := range []byte("literal/*value`") {
		if err := rawQuotedSuffix.consumeLongQuotedLineSuffix(b); err != nil {
			t.Fatalf("consumeLongQuotedLineSuffix raw suffix: %v", err)
		}
	}
	if rawQuotedSuffix.suffixInQuoted {
		t.Fatalf("expected raw-quoted suffix tracking to exit quoted mode")
	}
	if got := rawQuotedSuffix.longQuotedSuffix.String(); got != "`literal/*value`" {
		t.Fatalf("expected raw-quoted suffix to keep block-comment markers literal, got %q", got)
	}
}

func requireLongEscapedQuotedSuffix(t *testing.T) {
	t.Helper()
	var escapedQuotedSuffix goModModuleScanner
	escapedQuotedSuffix.lineTooLarge = true
	escapedQuotedSuffix.lineTooLargeInQuote = true
	escapedQuotedSuffix.lineQuoteClosed = true
	if err := escapedQuotedSuffix.consumeLongQuotedLineSuffix('"'); err != nil {
		t.Fatalf("consumeLongQuotedLineSuffix escaped quote: %v", err)
	}
	for _, b := range []byte("./a\\\"b\"") {
		if err := escapedQuotedSuffix.consumeLongQuotedLineSuffix(b); err != nil {
			t.Fatalf("consumeLongQuotedLineSuffix escaped suffix: %v", err)
		}
	}
	if escapedQuotedSuffix.suffixInQuoted {
		t.Fatalf("expected escaped quoted suffix tracking to exit quoted mode")
	}
	if got := escapedQuotedSuffix.longQuotedSuffix.String(); got != "\"./a\\\"b\"" {
		t.Fatalf("expected escaped quoted suffix to retain escapes, got %q", got)
	}
}

func requireLongRecoveryBlockLineGuards(t *testing.T) {
	t.Helper()
	rawQuotedOverflow := goModModuleScanner{
		inQuotedString:      true,
		quoteByte:           '`',
		lineTooLargeInQuote: true,
	}
	rawQuotedOverflow.consumeQuotedStringByte('`')
	if rawQuotedOverflow.inQuotedString || rawQuotedOverflow.quoteByte != 0 || !rawQuotedOverflow.lineQuoteClosed {
		t.Fatalf("expected oversized raw-quoted close to finish the long quoted target, got %#v", rawQuotedOverflow)
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
}

func requireLongDirectiveAcceptanceNeedsCompleteRecoveredState(t *testing.T) {
	t.Helper()

	quotedReplacement := goModModuleScanner{
		longQuotedQuote: '"',
		quoteStartValid: true,
		quoteStart:      len(`replace example.com/a => `),
		parseSynthetic:  func(string, string) bool { return true },
	}
	quotedReplacement.longQuotedTarget.WriteString("./replacement")
	quotedReplacement.longQuotedSuffix.WriteString(" // keep")
	if !quotedReplacement.acceptLongGoModDirectiveLine("replace", `replace example.com/a => "`, true, true) {
		t.Fatalf("expected complete quoted replace recovery to succeed")
	}

	unterminated := goModModuleScanner{
		longQuotedQuote: '"',
		quoteStartValid: true,
		quoteStart:      len(`replace example.com/a => `),
		parseSynthetic:  func(string, string) bool { return true },
	}
	unterminated.longQuotedTarget.WriteString("./replacement")
	if unterminated.acceptLongGoModDirectiveLine("replace", `replace example.com/a => "`, true, false) {
		t.Fatalf("expected unterminated quoted recovery to fail")
	}

	unquotedReplacement := goModModuleScanner{
		longUnquotedStart: len("replace example.com/a => "),
		parseSynthetic:    func(string, string) bool { return true },
	}
	unquotedReplacement.longUnquotedLine.WriteString("example.com/fork v1.2.3")
	if !unquotedReplacement.acceptLongGoModDirectiveLine("replace", "replace example.com/a => ", false, false) {
		t.Fatalf("expected unquoted replace recovery to succeed")
	}

	overflowedSynthetic := goModModuleScanner{
		longUnquotedStart: len("require "),
		parseSynthetic:    func(string, string) bool { return true },
	}
	overflowedSynthetic.syntheticBody.WriteString(strings.Repeat("x", maxGoModModuleScanBytes-1))
	overflowedSynthetic.longUnquotedLine.WriteString("example.com/dep v1.2.3")
	if overflowedSynthetic.acceptLongGoModDirectiveLine("require", "require ", false, false) {
		t.Fatalf("expected synthetic body overflow to fail closed")
	}
	if !overflowedSynthetic.invalid {
		t.Fatalf("expected synthetic body overflow to invalidate the scanner")
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
}

func requireQuotedStringAndRecoveryGuardsFailClosed(t *testing.T) {
	t.Helper()

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

	var suffixQuotedNewline goModModuleScanner
	suffixQuotedNewline.lineTooLarge = true
	suffixQuotedNewline.lineTooLargeInQuote = true
	suffixQuotedNewline.lineQuoteClosed = true
	if err := suffixQuotedNewline.consumeLongQuotedLineSuffix('"'); err != nil {
		t.Fatalf("consumeLongQuotedLineSuffix: %v", err)
	}
	if err := suffixQuotedNewline.consumeLongQuotedLineSuffix('x'); err != nil {
		t.Fatalf("consumeLongQuotedLineSuffix quoted body: %v", err)
	}
	if err := suffixQuotedNewline.consumeByte('\n'); err != nil {
		t.Fatalf("consumeByte newline: %v", err)
	}
	if !suffixQuotedNewline.invalid {
		t.Fatalf("expected quoted suffix newline to fail closed")
	}

	guardedUnquoted := goModModuleScanner{lineInvalid: true}
	guardedUnquoted.line.WriteString("default=go1.21")
	guardedUnquoted.startLongUnquotedLine()
	if guardedUnquoted.longUnquotedLine.Len() != 0 {
		t.Fatalf("expected invalid lines to skip long unquoted recovery")
	}
}

func TestGoModExceedsReadLimitReportsOversizedAndBoundedPaths(t *testing.T) {
	repo := t.TempDir()
	writeOversizedRootGoModLines(t, repo, "module example.com/root")

	oversized, err := goModExceedsReadLimit(repo, repo+"/go.mod", maxGoModBytes)
	if err != nil {
		t.Fatalf("goModExceedsReadLimit oversized: %v", err)
	}
	if !oversized {
		t.Fatalf("expected oversized go.mod to exceed read limit")
	}

	smallRepo := t.TempDir()
	writeRepoGoMod(t, smallRepo, "module example.com/root\n")
	oversized, err = goModExceedsReadLimit(smallRepo, smallRepo+"/go.mod", maxGoModBytes)
	if err != nil {
		t.Fatalf("goModExceedsReadLimit small: %v", err)
	}
	if oversized {
		t.Fatalf("expected small go.mod not to exceed read limit")
	}

	if _, err := goModExceedsReadLimit(repo, repo+"/missing.mod", maxGoModBytes); err == nil {
		t.Fatalf("expected missing go.mod to surface an error")
	}

	if _, err := goModExceedsReadLimit("\x00", "go.mod", maxGoModBytes); err == nil {
		t.Fatalf("expected invalid repo path to surface an error")
	}
}

func TestNormalizeInlineGoModRequireBlocksShortCircuitsWhenUnneeded(t *testing.T) {
	oversized := []byte(strings.Repeat("x", maxGoModBytes+1))
	if normalized := normalizeInlineGoModRequireBlocks(oversized); string(normalized) != string(oversized) {
		t.Fatalf("expected oversized content to bypass normalization")
	}

	stable := []byte("module example.com/root\nrequire example.com/dep v1.2.3\n")
	if normalized := normalizeInlineGoModRequireBlocks(stable); string(normalized) != string(stable) {
		t.Fatalf("expected already-stable content to bypass normalization")
	}
}

func TestHasValidRetractsRejectsInvalidModulePathMajor(t *testing.T) {
	const invalidPath = "example.com/root/v02"
	if _, _, ok := module.SplitPathVersion(invalidPath); ok {
		t.Fatalf("expected test fixture %q to fail SplitPathVersion", invalidPath)
	}
	file := &modfile.File{
		Module: &modfile.Module{Mod: module.Version{Path: invalidPath}},
		Retract: []*modfile.Retract{
			{VersionInterval: modfile.VersionInterval{Low: "v1.0.0"}},
		},
	}
	if hasValidRetracts(file) {
		t.Fatalf("expected invalid module path version split to fail retract validation")
	}
}

func scanRecoveredGoModFallback(lines ...string) (string, string, error) {
	var body string
	modulePath, err := scanGoModModulePathWithParser(strings.NewReader(strings.Join(lines, "\n")+"\n"), func(modulePath, recovered string) bool {
		body = recovered
		return true
	})
	return modulePath, body, err
}
