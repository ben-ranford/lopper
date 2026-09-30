package php

import "testing"

func TestDynamicPatternAlternatives(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{name: "variable constructor", code: "new $className();", want: true},
		{name: "multiline constructor", code: "new\n\t$_class();", want: true},
		{name: "variable static call", code: "$class_2 :: build();", want: true},
		{name: "class lookup", code: "class_exists($class);", want: true},
		{name: "interface lookup", code: "interface_exists ($interface);", want: true},
		{name: "trait lookup", code: "trait_exists\n($trait);", want: true},
		{name: "method lookup", code: "method_exists($object, $method);", want: true},
		{name: "static constructor", code: "new Service();"},
		{name: "static method call", code: "Service::build();"},
		{name: "lookup name suffix", code: "custom_class_exists($class);"},
		{name: "lookup without call", code: "$callback = class_exists;"},
		{name: "ordinary variable", code: "$className = Service::class;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dynamicPattern.MatchString(tt.code); got != tt.want {
				t.Fatalf("dynamicPattern.MatchString(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}
