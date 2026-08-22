package php

const (
	maxComposerManifestBytes int64 = 2 * 1024 * 1024
	maxComposerLockBytes     int64 = 8 * 1024 * 1024
	maxScannablePHPFile      int64 = 2 * 1024 * 1024

	maxPHPUseStatementsPerFile       = 4096
	maxPHPNamespaceReferencesPerFile = 4096
)
