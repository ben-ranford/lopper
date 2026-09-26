package reusecheck

// Migration exceptions freeze pre-adoption source; any edit requires review.
var legacyDigests = map[string]string{
	"internal/lang/python/reporting.go":        "bf70aea200b7cfaa91fd3d71d74ee28eb657517495b2861a23f44d7a05cb3c97",
	"internal/lang/dotnet/reporting.go":        "4d2af24b25b5f1e38d68ceba4161cf410cfc8cb9cff7f590abbbf3ddfb31eb2c",
	"internal/lang/rust/reporting.go":          "66db1cba75b65b331029e69773424c4f66e900abb818b4fb3a250743400406d9",
	"internal/lang/elixir/reporting.go":        "c2766a342dddb76246978fb15ea4a9ece94b75dd753bc5c4b9d41cbdf8d9525c",
	"internal/lang/golang/reporting.go":        "210416f153e29e8a9b29d45b85ada240f746b08cabf1a5263c76000ba268171f",
	"internal/lang/dart/reporting.go":          "0b3d6b0ba91c011acb4eb10e9ae460be4c7476aa9fdecef871ccef93b4e77c2b",
	"internal/lang/jvm/reporting.go":           "22a47723fc7d73256f406bf97f62364a059e379bb72a944a5d29162f0d12b651",
	"internal/lang/php/reporting.go":           "8a6c9a20f35e6b99c31bd91cf9c32eecd99fa4c0a3b52aaca5ae22f706307e84",
	"internal/lang/powershell/reporting.go":    "357c363e292eeb00e695faa4b299d2ff4a62fb03a55240c4dc76c838ee7e0444",
	"internal/analysis/identity_enrichment.go": "eb8b62b49a82a5c364162bb9798729b00ef907ee8fe14f4fc6c96d8a72d6f6e5",
	"internal/report/vulnerability.go":         "1fd61cc24a8aa99461daca3fcbbfd9b4ffc5679dd6cf3c946ff98c21cfab3aea",
}
