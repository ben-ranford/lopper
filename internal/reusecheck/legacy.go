package reusecheck

// Migration exceptions freeze pre-adoption source; any edit requires review.
var legacyDigests = map[string]string{
	"internal/lang/jvm/reporting.go":           "22a47723fc7d73256f406bf97f62364a059e379bb72a944a5d29162f0d12b651",
	"internal/lang/php/reporting.go":           "8a6c9a20f35e6b99c31bd91cf9c32eecd99fa4c0a3b52aaca5ae22f706307e84",
	"internal/lang/powershell/reporting.go":    "357c363e292eeb00e695faa4b299d2ff4a62fb03a55240c4dc76c838ee7e0444",
	"internal/analysis/identity_enrichment.go": "eb8b62b49a82a5c364162bb9798729b00ef907ee8fe14f4fc6c96d8a72d6f6e5",
	"internal/report/vulnerability.go":         "1fd61cc24a8aa99461daca3fcbbfd9b4ffc5679dd6cf3c946ff98c21cfab3aea",
}
