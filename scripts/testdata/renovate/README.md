# Marketplace version contract

`expectedMarketplaceVSCEVersion` in `scripts/release_workflow_config_test.go`
is an independent npm expectation. Its custom manager selects only the complete
named declaration in that file. The existing `VSCE tooling` rule groups the npm
manifest, four workflow pins and this expectation, including major updates.
Renovate's npm manager owns the lockfile. Dependency PRs require human review;
there are no post-upgrade commands.

Ordinary `go test ./scripts` checks the narrow matcher, all five pin cases and
small offline Node controls of the actual fixture guards and serial helper. Node
is already required by this suite; a missing Node is a setup failure. These
controls import only builtins, use synthetic boundary inputs, and require neither
Renovate nor npm installation, registry access or this optional provider. Native
paths and PATH delimiters are used; Windows link controls use ordinary directory
junctions, and the joined poison child runs builtin Node code. Windows offline
checks do not claim POSIX UID/mode checks prove Windows ACL ownership. The REAL
Darwin provider still requires every UID, non-writable and sealed-directory check.

The optional real fixture supports **Darwin arm64 only**. Other platforms fail
before integration effects. It preserves all nine real Renovate exports,
extraction negatives, six upgrades across exactly three package files, four
updated files, GlobalConfig, repeated workflow updates and ordered patch
3.9.1 -> 3.9.2 followed by major 3.9.2 -> 4.0.0. Each scenario regenerates genuine
npm locks with `--ignore-scripts`, runs the actual Go Marketplace contract, then
requires all four version/integrity corruptions to fail with its named assertion.
The valid lock is restored, and private fixture output is removed in `finally`.

## Official provider admission

Start from a reviewed checkout of this entry module and `runtime-integrity.json`.
Repository/source admission belongs to the outer runner; an entry module cannot
authenticate a hostile replacement of itself. Keep exclusive custody of the
checkout and new provider throughout acquisition and consumption. Filesystem
checks detect links/changes; they do not provide kernel isolation from concurrent
hostile writes by the same user.

The fixture derives its repository from its fixed module suffix and always uses
`.artifacts/renovate-vsce-runtime/{node,go,packages}` beneath that repository.
Optional positional repo/Renovate arguments only compare against these roots.
No argv, environment, cwd, receipt or personal SDK path selects a provider.
Historical `/tmp/lopper-1669-renovate` invocations remain unchanged in their
receipts. Future runs migrate to this provider. Existing package/cache bytes may
be reused only after comparison with independently obtained official identities.
Do not rewrite the historical installation or global configuration.

The shipped manifest pins Node 24.21.0 and Go 1.27.2 official Darwin arm64 archives,
Renovate 44.145.1, the exact 618-package graph/layout, published npm SHA-512
integrities and RE2's official 1.27.0 ABI 137 release asset. Expected inventories
were derived from these authenticated archives, then compared with all 40,604
consumed files of the existing installation. They include package.json/exports,
dist, transitive files and the native binary. Versions and first-use hashes are
insufficient admission.

Inventory encoding is UTF-8 JSON lines, sorted by relative POSIX path using code
point order: `[path,type,executableBit,length,sha256]`. Each line ends in LF;
directories use `"d",0,0,""`; regular files use `"f"` and their content SHA-256.
The root itself is excluded. SHA-256 binds the entire stream plus its entry count.
Links/special files and unexpected members fail. Ownership and non-group/world
writability are checked separately; executable directories are sealed during use.

Exact exclusions: Node's unused `bin/npm`, `bin/npx`, `bin/corepack` symlink aliases;
all historical npm `.bin` aliases (none is provisioned or exposed on PATH);
historical npm installation metadata `.package-lock.json`; unrelated npm `node`
and `node-bin-setup` launchers; generated dtrace-provider object/build scratch
files. The provider retains every official dtrace-provider archive member; its
historical installation had no native `.node`. RE2 is the sole added native
member and is authenticated against its published release checksum. No consumed
native/generated member is silently omitted. Missing verified inputs stop that
provider's execution.

Run this complete acquisition/admission recipe from the reviewed repository root.
It uses Python's standard library and the admitted official Node for builtin
Brotli decompression. It executes no package installer or lifecycle script and
creates the canonical, owned, non-link `.artifacts` parent when absent and writes
only the new provider beneath it. It refuses to replace an existing provider.
Capture this recipe's admission output and any raw failure outside the provider
in the runner's immutable evidence packet. Acquisition failure removes only the
provider this invocation exclusively created; it never removes `.artifacts`.

```sh
python3 - <<'PY'
from pathlib import Path, PurePosixPath
import base64, hashlib, io, json, os, platform, shutil, stat, subprocess
import tarfile, traceback, urllib.parse, urllib.request
assert platform.system() == 'Darwin' and platform.machine() == 'arm64'
repo = Path.cwd().resolve()
manifest = json.loads((repo/'scripts/testdata/renovate/runtime-integrity.json').read_text())
artifacts = repo/'.artifacts'
if not os.path.lexists(artifacts): artifacts.mkdir(mode=0o700)
info = artifacts.lstat()
assert stat.S_ISDIR(info.st_mode) and not artifacts.is_symlink()
assert artifacts.resolve() == artifacts and info.st_uid == os.getuid() and not info.st_mode & 0o022
provider = artifacts/'renovate-vsce-runtime'
def cleanup_owned_provider():
    # This invocation exclusively created this exact provider; never adopt an existing one.
    expected = repo/'.artifacts/renovate-vsce-runtime'
    assert provider == expected and provider.resolve() == expected
    info = provider.lstat()
    assert stat.S_ISDIR(info.st_mode) and not provider.is_symlink() and info.st_uid == os.getuid()
    members = [provider, *provider.rglob('*')]
    for path in members:
        info = path.lstat()
        assert info.st_uid == os.getuid() and not path.is_symlink()
        assert stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)
    for path in members:
        info = path.lstat()
        if stat.S_ISDIR(info.st_mode): path.chmod(stat.S_IMODE(info.st_mode) | 0o700)
    shutil.rmtree(provider)
    assert not os.path.lexists(provider)
provider.mkdir(mode=0o700)  # Exclusive creation: existing providers are never replaced.
try:
    downloads = provider/'.downloads'; downloads.mkdir(mode=0o700)
    def fetch(url):
        assert url.startswith(('https://nodejs.org/', 'https://go.dev/',
                               'https://registry.npmjs.org/', 'https://api.github.com/',
                               'https://github.com/uhop/node-re2/'))
        return urllib.request.urlopen(url, timeout=60).read()
    def digest(data): return hashlib.sha256(data).hexdigest()
    def unpack(data, target, excluded=()):
        target.mkdir(parents=True, exist_ok=True)
        with tarfile.open(fileobj=io.BytesIO(data)) as archive:
            members = archive.getmembers(); prefix = PurePosixPath(members[0].name).parts[0]
            for member in members:
                parts = PurePosixPath(member.name).parts
                assert parts[0] == prefix and '..' not in parts and not member.name.startswith('/')
                leaf = '/'.join(parts[1:])
                if not leaf or leaf in excluded: continue
                assert member.isfile() or member.isdir(), member.name
                path = target/leaf
                if member.isdir(): path.mkdir(parents=True, exist_ok=True)
                else:
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(archive.extractfile(member).read())
                    path.chmod(0o755 if member.mode & 0o111 else 0o644)
    def inventory(root):
        rows = []
        for path in root.rglob('*'):
            info = path.lstat(); assert not path.is_symlink()
            leaf = path.relative_to(root).as_posix()
            if path.is_dir(): rows.append([leaf, 'd', 0, 0, ''])
            else:
                assert stat.S_ISREG(info.st_mode)
                rows.append([leaf, 'f', int(bool(info.st_mode & 0o111)), info.st_size, digest(path.read_bytes())])
        stream = ''.join(json.dumps(row, separators=(',', ':'), ensure_ascii=False)+'\n'
                         for row in sorted(rows, key=lambda row: row[0])).encode()
        return dict(entries=len(rows), sha256=digest(stream))
    node = manifest['node']; go = manifest['go']
    shasums = fetch('https://nodejs.org/dist/v24.21.0/SHASUMS256.txt').decode()
    assert node['sha256']+'  node-v24.21.0-darwin-arm64.tar.gz' in shasums
    go_release = json.loads(fetch('https://go.dev/dl/?mode=json&include=all'))
    assert any(file['filename'] == 'go1.27.2.darwin-arm64.tar.gz' and file['sha256'] == go['sha256']
               for release in go_release if release['version'] == 'go1.27.2' for file in release['files'])
    for name, pin in [('node', node), ('go', go)]:
        data = fetch(pin['url']); assert digest(data) == pin['sha256']
        unpack(data, provider/name, pin.get('excludedLinks', []))
        assert inventory(provider/name) == pin['inventory']
    for pin in manifest['packages']['graph']:
        parts = PurePosixPath(pin['path']).parts
        assert parts[0] == 'node_modules' and '..' not in parts
        url = 'https://registry.npmjs.org/'+urllib.parse.quote(pin['name'], safe='@')+'/'+pin['version']
        published = json.loads(fetch(url))['dist']
        assert published['integrity'] == pin['integrity'] and published['tarball'] == pin['resolved']
        algorithm, encoded = pin['integrity'].split('-', 1); assert algorithm == 'sha512'
        key = base64.b64decode(encoded).hex()
        cached = Path.home()/'.npm/_cacache/content-v2/sha512'/key[:2]/key[2:4]/key[4:]
        data = cached.read_bytes() if cached.is_file() and not cached.is_symlink() else fetch(pin['resolved'])
        assert base64.b64encode(hashlib.sha512(data).digest()).decode() == encoded
        unpack(data, provider/'packages'/pin['path'])
    native = manifest['native']
    release = json.loads(fetch('https://api.github.com/repos/uhop/node-re2/releases/tags/1.27.0'))
    assert any(asset['browser_download_url'] == native['url'] and asset['digest'] == 'sha256:'+native['sha256']
               for asset in release['assets'])
    compressed = fetch(native['url']); assert digest(compressed) == native['sha256']
    asset = downloads/'re2.br'; asset.write_bytes(compressed)
    assert inventory(provider/'node') == node['inventory']  # Before first Node execution.
    script = 'import fs from "node:fs"; import z from "node:zlib"; process.stdout.write(z.brotliDecompressSync(fs.readFileSync(process.argv[1])));'
    data = subprocess.check_output([provider/'node/bin/node', '--input-type=module', '-e', script, str(asset)],
                                   env={'PATH':'/usr/bin:/bin', 'HOME':str(downloads)})
    assert digest(data) == native['contentSHA256']
    output = provider/'packages'/native['path']; output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(data); output.chmod(0o644)
    shutil.rmtree(downloads)
    for name in ['node', 'go', 'packages']:
        root = provider/name
        assert inventory(root) == manifest[name]['inventory']
        for path in root.rglob('*'):
            assert path.lstat().st_uid == os.getuid() and not path.is_symlink()
            path.chmod(0o555 if path.is_dir() or path.stat().st_mode & 0o111 else 0o444)
        root.chmod(0o555)
        assert inventory(root) == manifest[name]['inventory']
    print(json.dumps({'officialAdmission': {name: inventory(provider/name) for name in ['node', 'go', 'packages']},
                      'provider': str(provider), 'exclusiveCustody': True}, sort_keys=True))
except BaseException:
    # Emit the raw acquisition failure to the outer receipt capture before teardown.
    try: traceback.print_exc()
    finally: cleanup_owned_provider()
    raise
PY
```

## Invoke and clean up

The trusted outer runner sanitizes Node **before startup**. Clearing NODE_OPTIONS
inside a fixture cannot undo a preload that already ran. Use the direct admitted
Node and regular bundled npm CLI; do not recreate npm aliases or use a shebang.
For example, from the reviewed root after the admission above:

The same owner keeps exclusive custody, captures admission and final execution /
pre-post custody receipts outside the provider, and always tears down this new
provider after receipts, including invocation failure. A complete invocation and
teardown example (use only the provider exclusively created above):

```sh
python3 - <<'PY'
from pathlib import Path
import json, os, shutil, stat, subprocess, tempfile, traceback
repo = Path.cwd().resolve()
artifacts = repo/'.artifacts'
info = artifacts.lstat()
assert artifacts.resolve() == artifacts and stat.S_ISDIR(info.st_mode)
assert not artifacts.is_symlink() and info.st_uid == os.getuid() and not info.st_mode & 0o022
provider = repo/'.artifacts/renovate-vsce-runtime'
def cleanup_owned_provider():
    # This invocation exclusively created this exact provider; never adopt an existing one.
    expected = repo/'.artifacts/renovate-vsce-runtime'
    assert provider == expected and provider.resolve() == expected
    info = provider.lstat()
    assert stat.S_ISDIR(info.st_mode) and not provider.is_symlink() and info.st_uid == os.getuid()
    members = [provider, *provider.rglob('*')]
    for path in members:
        info = path.lstat()
        assert info.st_uid == os.getuid() and not path.is_symlink()
        assert stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)
    for path in members:
        info = path.lstat()
        if stat.S_ISDIR(info.st_mode): path.chmod(stat.S_IMODE(info.st_mode) | 0o700)
    shutil.rmtree(provider)
    assert not os.path.lexists(provider)
receipts = None
try:
    # This fresh receipt directory survives provider cleanup; no SDK copy is retained.
    receipts = Path(tempfile.mkdtemp(prefix='vsce-receipts-', dir=artifacts))
    manifest = (repo/'scripts/testdata/renovate/runtime-integrity.json').read_bytes()
    (receipts/'admitted-manifest.json').write_bytes(manifest)
    result = subprocess.run([provider/'node/bin/node', repo/'scripts/testdata/renovate/vsce-contract.mjs'],
                            env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin'}, capture_output=True)
    (receipts/'real.stdout').write_bytes(result.stdout)
    (receipts/'real.stderr').write_bytes(result.stderr)
    (receipts/'result.json').write_text(json.dumps({'exit': result.returncode,
        'provider': str(provider), 'entryPrePostAdmission': result.returncode == 0})+'\n')
    result.check_returncode()
except BaseException:
    if receipts is not None: (receipts/'failure.txt').write_text(traceback.format_exc())
    raise
finally:
    try:
        if receipts is not None:
            for path in receipts.iterdir(): path.chmod(0o444)
            receipts.chmod(0o555)
            print('Retained immutable receipts:', receipts)
    finally:
        cleanup_owned_provider()
PY
```

The fixture checks complete inventories immediately before imports and after
consumption, and compares device/inode/owner/mode identities for every consumed
member across the run. Those per-run observations are not shipped pins. It admits Darwin Git at
`/Library/Developer/CommandLineTools/usr/bin/git` and `/usr/bin/bsdtar` using
canonical regular/root-owned/non-writable OS roots, Apple code-signing authority
and the installed CLT package receipt, including Git's archive libexec. It relies
on trusted OS/system-library installation; observed personal tool hashes are not
source pins. Git archive HEAD and actual tar extraction remain unchanged; tracked
paths and actual archive member names/types are inspected before extraction.
Copies and every returned upgrade path are checked before I/O.

Imports and children share a scoped allowlisted environment: admitted Node/Go
bins plus system bins, private 0700 fixture HOME/npm config/cache, public npm
registry, official GOROOT, local toolchain and isolated Go cache/module access.
Caller Node/Git/npm/Go/loader/shell overrides are removed, and parent environment
is restored in `finally`. The fixture makes no GitHub writes. Its temporary
output is removed on success or failure. Read-only directories created by Go in
that private fixture's module cache are made owner-writable during cleanup without
following links; external caches are untouched. Retain immutable official
admission, final execution and pre/post custody receipts, then remove the whole
NEW owned provider before general repository duplication/scanner gates. This
includes acquisition and invocation failure. The self-contained guarded cleanup
above validates the exact canonical root, directory type, owner and every member
before making only owned directories writable, removing the whole provider and
checking its absence. Never remove individual official SDK corpus files, an
existing provider, historical installations, caches, receipts or `.artifacts`.
A source-only zero-finding scan does not supersede a failed repository CLI run.
The runner must confirm provider absence before that later unchanged CLI run.

Reviewed upstream: [regex manager](https://docs.renovatebot.com/modules/manager/regex/),
[grouping](https://docs.renovatebot.com/configuration-options/#groupname),
[validation](https://docs.renovatebot.com/config-validation/),
[npm ignore-scripts](https://docs.npmjs.com/cli/v11/commands/npm-install/#ignore-scripts),
[Node checksums](https://nodejs.org/dist/v24.21.0/SHASUMS256.txt),
[Go release checksums](https://go.dev/dl/?mode=json&include=all),
[npm Renovate 44.145.1 identity](https://registry.npmjs.org/renovate/44.145.1), and
[RE2 native release](https://github.com/uhop/node-re2/releases/tag/1.27.0).
Renovate's reviewed release is
[44.145.1](https://github.com/renovatebot/renovate/releases/tag/44.145.1)
(commit `0dfb75020092789b7dc3f398e31259173f24b69f`). The hosted bot's deployed
version remains unverified; this fixture proves only the pinned local release.
