package php

import "testing"

func TestParseShortOpenTagSettingDirectiveOrderAndSections(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		scoped     bool
		enabled    bool
		found      bool
		incomplete bool
	}{
		{name: "empty and comments", content: "\n ; short_open_tag=on\n # short_open_tag=on"},
		{name: "mixed directive order", content: "short_open_tag=off\nPHP_FLAG SHORT_OPEN_TAG ON", enabled: true, found: true},
		{name: "ini overrides apache", content: "php_value short_open_tag on\nSHORT_OPEN_TAG = off", found: true},
		{name: "malformed apache ignores equals", content: "short_open_tag=on\nphp_value short_open_tag=off", enabled: true, found: true},
		{name: "unrelated and incomplete directives", content: "short_open_tag=on\nphp_flag\nphp_value other off\nother=off\nunknown", enabled: true, found: true},
		{name: "unknown final value", content: "short_open_tag=on\nphp_flag short_open_tag maybe", incomplete: true},
		{name: "resolved final value", content: "short_open_tag=maybe\nphp_value short_open_tag off", found: true},
		{name: "nested scoped apache", content: "<IfModule php>\n<Files *.php>\nphp_flag short_open_tag on\n</Files>\n</IfModule>\nshort_open_tag=off", scoped: true, incomplete: true},
		{name: "nested scoped ini", content: "<IfModule php>\n<Files *.php>\n</Files>\nshort_open_tag=off", scoped: true, incomplete: true},
		{name: "closed unrelated sections", content: "<IfModule rewrite>\n<Files *.php>\nother=on\n</Files>\n</IfModule>\nshort_open_tag=on", scoped: true, enabled: true, found: true},
		{name: "unmatched closing section", content: "</Files>\n<Files *.php>\nshort_open_tag=on", scoped: true, incomplete: true},
		{name: "ini ignores apache sections", content: "<Files *.php>\nshort_open_tag=on", enabled: true, found: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled, found, incomplete := parseShortOpenTagSetting(tt.content, tt.scoped)
			if enabled != tt.enabled || found != tt.found || incomplete != tt.incomplete {
				t.Fatalf("got (%v, %v, %v), want (%v, %v, %v)", enabled, found, incomplete, tt.enabled, tt.found, tt.incomplete)
			}
		})
	}
}
