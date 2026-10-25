package scripts

import (
	"flag"
	"testing"
)

func TestFixtureParallelismPreservesCallerOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{name: "fixture default", want: 16},
		{name: "serial caller", args: []string{"-test.parallel=1"}, want: 1},
		{name: "larger caller budget", args: []string{"-test.parallel=24"}, want: 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			parallel := flags.Int("test.parallel", 4, "parallel test budget")
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if err := configureFixtureParallelism(flags); err != nil {
				t.Fatal(err)
			}
			if *parallel != tc.want {
				t.Fatalf("parallel budget = %d, want %d", *parallel, tc.want)
			}
		})
	}
}
