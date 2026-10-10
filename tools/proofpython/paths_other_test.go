//go:build !windows

package main

import "testing"

func TestDeviceIdentity(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		want    uint64
		wantErr string
	}{
		{name: "negative signed device", value: int32(-1), wantErr: "negative device identity"},
		{name: "zero signed device", value: int32(0), want: 0},
		{name: "maximum signed device", value: int32(1<<31 - 1), want: uint64(1<<31 - 1)},
		{name: "maximum uint32 device", value: uint32(1<<32 - 1), want: uint64(1<<32 - 1)},
		{name: "maximum uint64 device", value: ^uint64(0), want: ^uint64(0)},
		{name: "unsupported device type", value: int(1), wantErr: "unsupported device identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deviceIdentity(tt.value)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("deviceIdentity(%T(%v)) error = %v, want %q", tt.value, tt.value, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("deviceIdentity(%T(%v)) error = %v", tt.value, tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("deviceIdentity(%T(%v)) = %d, want %d", tt.value, tt.value, got, tt.want)
			}
		})
	}
}
