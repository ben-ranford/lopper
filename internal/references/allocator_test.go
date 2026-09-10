package references

import "testing"

func TestAllocatorSkipsReservedAndUsedSuffixCollisions(t *testing.T) {
	allocator := NewAllocator([]string{
		"lopper:repo:api",
		"lopper:repo:api:2",
		"lopper:repo:api:3",
	})

	if got := allocator.Allocate("lopper:repo:api"); got != "lopper:repo:api" {
		t.Fatalf("first Allocate() = %q, want base ref", got)
	}
	if got := allocator.Allocate("lopper:repo:api"); got != "lopper:repo:api:4" {
		t.Fatalf("second Allocate() = %q, want first free suffix after reserved collisions", got)
	}
	if got := allocator.Allocate("lopper:repo:api"); got != "lopper:repo:api:5" {
		t.Fatalf("third Allocate() = %q, want suffix after reserved and used collisions", got)
	}
}
