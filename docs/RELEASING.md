# Releasing Ta-Da

This page is for maintainers. It covers the release workflow, the secrets
it uses, and how to set each one up.

## How a release runs

Push a tag like `v0.6.0`. `.github/workflows/release.yml` then:

1. Builds the six binaries (macOS, Linux and Windows; x86_64 and arm64).
2. Signs and notarizes the macOS binaries, if the Apple secrets are set.
3. Signs the Windows `tada.exe` files, if the Windows secrets are set.
4. Packages the archives and writes `SHA256SUMS.txt`. Signing comes first,
   so the archives and checksums cover the signed binaries.
5. Drafts the GitHub release from the matching `CHANGELOG.md` section.
6. Builds the Windows installers (`windows-installer.yml`), signs them if
   the Windows secrets are set, and publishes the release.
7. Updates Homebrew, Scoop and winget (`package-managers.yml`).

Every secret is optional. A missing secret skips its step with a notice
in the run log, and the release still goes out. With no secrets at all
you get the same unsigned release as before.

Tags with a hyphen (like `v0.6.0-rc.1`) are prereleases. They are
published on GitHub but skip the package managers.

A manual run (Actions > release > Run workflow) takes an existing tag:

- `dry_run` builds, signs and packages, then uploads `dist` as a run
  artifact. It touches no release and no package manager. With the Apple
  API key set, it still submits the binaries to Apple's notary service.
- `publish` off leaves a draft release and skips the package managers.

You can also re-run `windows-installer` or `package-managers` by hand
for a published tag.

## Secrets

Set these under Settings > Secrets and variables > Actions in
`ItsCetla/tada`, or with `gh secret set NAME`.

| Secret | Used for |
|---|---|
| `APPLE_CERT_P12_BASE64` | Developer ID Application certificate and key (.p12, base64) |
| `APPLE_CERT_PASSWORD` | Password of that .p12 |
| `APPLE_API_KEY_ID` | App Store Connect API key ID (notarization) |
| `APPLE_API_ISSUER_ID` | App Store Connect issuer ID (notarization) |
| `APPLE_API_KEY_P8_BASE64` | The `AuthKey_<id>.p8` file (base64) |
| `WINDOWS_CERT_PFX_BASE64` | Code-signing certificate and key (.pfx, base64) |
| `WINDOWS_CERT_PASSWORD` | Password of that .pfx |
| `HOMEBREW_TAP_TOKEN` | Push access to `ItsCetla/homebrew-tap` |
| `SCOOP_BUCKET_TOKEN` | Push access to `ItsCetla/scoop-bucket` |
| `WINGET_TOKEN` | Opening pull requests on `microsoft/winget-pkgs` |

Optional repository variables (same page, Variables tab):

| Variable | Default |
|---|---|
| `HOMEBREW_TAP_REPO` | `ItsCetla/homebrew-tap` |
| `SCOOP_BUCKET_REPO` | `ItsCetla/scoop-bucket` |
| `WINDOWS_TIMESTAMP_URL` | `http://timestamp.digicert.com` |

To store a file as a base64 secret:

```sh
base64 -i cert.p12 | gh secret set APPLE_CERT_P12_BASE64     # macOS
base64 -w0 cert.pfx | gh secret set WINDOWS_CERT_PFX_BASE64  # Linux
```

## macOS signing and notarization

Signing runs on Linux with
[rcodesign](https://github.com/indygreg/apple-platform-rs), so no Mac
runner is needed. The workflow downloads a pinned release and checks its
SHA-256 first. The logic lives in `scripts/sign-macos.sh`.

You need a paid Apple Developer Program membership.

### Certificate

1. On a Mac, open Keychain Access. Choose Certificate Assistant > Request
   a Certificate From a Certificate Authority. Save the request to disk.
2. In the Apple Developer site, go to Certificates and add a
   **Developer ID Application** certificate. Only the account holder can
   create one. Upload the request and download the certificate.
3. Double-click the certificate to add it to Keychain Access.
4. In My Certificates, select it together with its private key. Export
   them as a .p12 file with a password.
5. Save the file as `APPLE_CERT_P12_BASE64` (base64) and the password as
   `APPLE_CERT_PASSWORD`.

The binaries are signed with the hardened runtime, a secure timestamp,
and the identifier `dev.cetla.tada`.

### Notarization key

1. In App Store Connect, open Users and Access > Integrations > App Store
   Connect API. Generate a team key with the **Developer** role.
2. Download `AuthKey_<id>.p8`. Apple only lets you download it once.
3. Save the key ID as `APPLE_API_KEY_ID`, the issuer ID shown above the
   key list as `APPLE_API_ISSUER_ID`, and the .p8 file (base64) as
   `APPLE_API_KEY_P8_BASE64`.

With the certificate but no key, the binaries are signed but not
notarized.

A bare binary cannot have its ticket stapled. macOS looks the ticket up
online the first time it checks the binary. Files fetched with `curl` or
installed by Homebrew are not quarantined, so Gatekeeper does not check
them at all. Notarization matters most for archives downloaded in a
browser.

To check a signed binary on a Mac:

```sh
codesign --verify --strict --verbose=2 tada
codesign -dv --verbose=4 tada   # look for "Developer ID Application"
```

## Windows signing

`tada.exe` is signed on Linux with
[osslsigncode](https://github.com/mtrojnar/osslsigncode) before it is
zipped (`scripts/sign-windows.sh`). The installers are signed on the
Windows runner with `signtool`, through Inno Setup's `SignTool` setting,
which also signs the uninstaller. Both use SHA-256 and an RFC 3161
timestamp, so the signatures stay valid after the certificate expires.

Save the certificate and private key as a .pfx (PKCS #12) file. Store it
base64-encoded as `WINDOWS_CERT_PFX_BASE64` and its password as
`WINDOWS_CERT_PASSWORD`. In PowerShell:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("cert.pfx")) | gh secret set WINDOWS_CERT_PFX_BASE64
```

**The key must be exportable.** Since June 2023, public CAs only issue
code-signing certificates whose keys live on hardware: a USB token, an
HSM, or the CA's cloud signing service. Those keys cannot be exported
to a .pfx. So this path suits a certificate whose key you hold as a file,
such as an older certificate or a self-signed one for testing.

For a new certificate, a cloud signing service fits CI better. One
option is Microsoft's Azure Trusted Signing. The workflows do not support
it yet: it needs its own sign step in place of the .pfx steps. Check its
current eligibility rules before buying anything.

SmartScreen may still warn about a newly signed file. The warnings stop
as the certificate builds reputation.

## Homebrew

`scripts/render-homebrew.sh` writes `Formula/tada.rb`. It covers macOS
and Linux on both architectures and installs the man page and shell
completions.

1. Create a public repository named `ItsCetla/homebrew-tap`. Add a README
   so it has a default branch. The workflow creates `Formula/` itself.
2. Create a fine-grained personal access token. Resource owner:
   ItsCetla. Repository access: only `homebrew-tap`. Permissions:
   Contents read and write.
3. Save it as `HOMEBREW_TAP_TOKEN`.

Users install with:

```sh
brew install ItsCetla/tap/tada
```

## Scoop

`scripts/render-scoop.sh` writes `bucket/tada.json` for the portable
zips. It includes `checkver` and `autoupdate`, so Scoop's own tools can
bump it too.

1. Create a public repository named `ItsCetla/scoop-bucket` with a
   README.
2. Create a fine-grained token as for Homebrew, limited to
   `scoop-bucket`.
3. Save it as `SCOOP_BUCKET_TOKEN`.

Users install with:

```powershell
scoop bucket add itscetla https://github.com/ItsCetla/scoop-bucket
scoop install itscetla/tada
```

## winget

`scripts/render-winget.sh` writes the three manifests for
`ItsCetla.TaDa` (version, installer and en-US locale, schema 1.12.0).
They point at the Inno Setup installers and install per user with
`/CURRENTUSER`. Every run uploads them as the `winget-manifests`
artifact. With a token,
[Komac](https://github.com/russellbanks/Komac) (pinned and checked by
SHA-256) forks `microsoft/winget-pkgs` and opens a pull request.

1. Create a **classic** personal access token with the `public_repo`
   scope. A fine-grained token cannot open pull requests on another
   owner's repository. The pull request comes from the token owner's
   fork.
2. Save it as `WINGET_TOKEN`.

The first submission of a new package is reviewed by the winget-pkgs
moderators and can take a few days. Their pipeline installs the package
in a sandbox. Unsigned installers are more likely to be flagged there.
Later versions go through the same pull request flow, usually faster.

Keep `ItsCetla.TaDa` as the package ID. Keep the `AppId` in
`packaging/windows/tada.iss` too: the manifests use it as the product
code, and upgrades depend on it.

To submit by hand, download the `winget-manifests` artifact, then run
either:

```sh
komac submit manifests/i/ItsCetla/TaDa/<version>
wingetcreate submit manifests/i/ItsCetla/TaDa/<version>
```

To test the manifests on Windows first:

```powershell
winget validate --manifest manifests\i\ItsCetla\TaDa\<version>
winget settings --enable LocalManifestFiles   # once, as admin
winget install --manifest manifests\i\ItsCetla\TaDa\<version>
```

Users install with:

```powershell
winget install ItsCetla.TaDa
```
