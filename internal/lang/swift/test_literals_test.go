package swift

import (
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	swiftSourcesDirName = "Sources"
	swiftAppDirName     = "App"
	swiftDemoDirName    = "Demo"

	swiftDemoPackageName = "Demo"

	alamofireFixtureName          = "alamofire"
	alamofireRepositoryURL        = "https://github.com/Alamofire/Alamofire.git"
	alamofireVersion              = "5.8.0"
	alamofirePodVersion           = "5.8.1"
	alamofireProductName          = "Alamofire"
	kingfisherFixtureName         = "kingfisher"
	kingfisherRepositoryURL       = "https://github.com/onevcat/Kingfisher.git"
	kingfisherVersion             = "7.9.0"
	kingfisherProductName         = "Kingfisher"
	swiftCollectionsRepositoryURL = "https://github.com/apple/swift-collections.git"
	swiftCollectionsVersion       = "1.1.0"

	swiftImportFoundationSource       = "import Foundation\n"
	swiftImportAlamofireSource        = "import Alamofire\n"
	swiftImportSwiftCollectionsSource = "import SwiftCollections\n"

	swiftAlamofireSessionUsageSource = `import Alamofire
func run() {
  _ = Session.default
}`
	swiftAlamofireSessionValueSource = `import Alamofire
let value = Session.default`
	swiftAlamofireLocalThingUsageSource = `import Alamofire
struct LocalThing {
  let id: String
}
func run() {
  let thing = LocalThing(id: "1")
  _ = thing.id
}`
	swiftKingfisherSharedUsageSource = `import Kingfisher
_ = KingfisherManager.shared`

	swiftResolvedRevision       = "abc"
	swiftPodfilePlatformVersion = "16.0"
)

func swiftSourceFilePath(repo, sourceDir string) string {
	return filepath.Join(repo, swiftSourcesDirName, sourceDir, swiftMainFileName)
}

func writeSwiftAppSourceFile(t *testing.T, repo string, mainContent string) {
	t.Helper()
	testutil.MustWriteFile(t, swiftSourceFilePath(repo, swiftAppDirName), mainContent)
}

func writeSwiftDemoSourceFile(t *testing.T, repo string, mainContent string) {
	t.Helper()
	testutil.MustWriteFile(t, swiftSourceFilePath(repo, swiftDemoDirName), mainContent)
}

func kingfisherFixtureDependency() swiftFixtureDependency {
	return swiftFixtureDependency{
		identity:    kingfisherFixtureName,
		url:         kingfisherRepositoryURL,
		version:     kingfisherVersion,
		productName: kingfisherProductName,
	}
}

func kingfisherPodFixtureDependency() swiftFixturePodDependency {
	return swiftFixturePodDependency{
		name:    kingfisherProductName,
		version: kingfisherVersion,
	}
}
