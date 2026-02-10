package ui

import "context"

type TUI interface {
	Start(ctx context.Context, opts Options) error
	Snapshot(ctx context.Context, opts Options, outputPath string) error
}
