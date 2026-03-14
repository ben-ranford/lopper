package rust

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkParseImportBytes(b *testing.B) {
	content := benchmarkRustImportContent(200)
	lookup := map[string]dependencyInfo{
		"anyhow": {Canonical: "anyhow"},
		"serde":  {Canonical: "serde"},
		"tokio":  {Canonical: "tokio"},
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	for i := 0; i < b.N; i++ {
		imports := parseRustImportsBytes(content, srcLibRS, "", lookup, nil)
		if len(imports) == 0 {
			b.Fatal("expected parsed imports")
		}
	}
}

func benchmarkRustImportContent(statements int) []byte {
	var builder strings.Builder
	for i := 0; i < statements; i++ {
		fmt.Fprintf(&builder, "extern crate serde as serde_alias_%d;\n", i)
		fmt.Fprintf(&builder, "use serde::{de::{DeserializeOwned, Visitor}, ser::SerializeMap, *};\n")
		fmt.Fprintf(&builder, "use tokio::{io::{AsyncReadExt, AsyncWriteExt}, sync::mpsc::{self, Sender}};\n")
		fmt.Fprintf(&builder, "use anyhow::Result;\n")
		fmt.Fprintf(&builder, "fn run_%d() {}\n", i)
	}
	return []byte(builder.String())
}
