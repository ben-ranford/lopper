package php

const (
	maxComposerManifestBytes int64 = 2 * 1024 * 1024
	maxComposerLockBytes     int64 = 8 * 1024 * 1024
	maxScannablePHPFile      int64 = 2 * 1024 * 1024
	maxPHPConfigBytes        int64 = 64 * 1024
	maxPHPConfigWalkEntries        = maxScanFiles

	maxPHPUseStatementsPerFile       = 4096
	maxPHPNamespaceReferencesPerFile = 4096
	maxPHPNamespaceSegmentsPerLookup = 256
	maxPHPNamespaceAncestorBytes     = 64 * 1024
)
