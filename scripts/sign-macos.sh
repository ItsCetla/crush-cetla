#!/usr/bin/env bash
# Signs the two darwin binaries in BIN_DIR in place with rcodesign (Developer
# ID Application certificate, hardened runtime, Apple timestamp), then
# notarizes them. Runs on Linux or macOS; no Xcode needed. Used by
# .github/workflows/release.yml before packaging, so the archives and
# SHA256SUMS.txt cover the signed binaries.
#
#   scripts/sign-macos.sh BIN_DIR
#
# BIN_DIR holds tada_darwin_amd64 and tada_darwin_arm64. Settings come from
# the environment (see docs/RELEASING.md):
#
#   APPLE_CERT_P12_BASE64    Developer ID Application cert + key, .p12, base64
#   APPLE_CERT_PASSWORD      Password of that .p12
#   APPLE_API_ISSUER_ID      App Store Connect API key issuer ID
#   APPLE_API_KEY_ID         App Store Connect API key ID
#   APPLE_API_KEY_P8_BASE64  The AuthKey_<id>.p8 file, base64
#   RCODESIGN                rcodesign binary (default: rcodesign on PATH)
#
# No certificate: does nothing. Certificate but no API key: signs and skips
# notarization. A bare binary cannot be stapled; macOS looks the ticket up
# online the first time it checks the binary.
set -euo pipefail

bin_dir=${1:?bin dir}
rcodesign=${RCODESIGN:-rcodesign}
identifier=${APPLE_BINARY_IDENTIFIER:-dev.cetla.tada}

notice() {
	if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::notice::$*"; else echo "$*"; fi
}
decode() { tr -d ' \r\n' | base64 -d; }

if [ -z "${APPLE_CERT_P12_BASE64:-}" ]; then
	notice "macOS signing skipped: APPLE_CERT_P12_BASE64 is not set"
	exit 0
fi

bins=()
for arch in amd64 arm64; do
	f="$bin_dir/tada_darwin_$arch"
	[ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
	bins+=("$f")
done

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf '%s' "$APPLE_CERT_P12_BASE64" | decode >"$work/cert.p12"
printf '%s' "${APPLE_CERT_PASSWORD:-}" >"$work/cert.pass"

for f in "${bins[@]}"; do
	"$rcodesign" sign \
		--p12-file "$work/cert.p12" \
		--p12-password-file "$work/cert.pass" \
		--binary-identifier "$identifier" \
		--code-signature-flags runtime \
		--for-notarization \
		"$f"
	"$rcodesign" verify "$f"
done
echo "Signed ${bins[*]}"

if [ -z "${APPLE_API_KEY_ID:-}" ] || [ -z "${APPLE_API_ISSUER_ID:-}" ] ||
	[ -z "${APPLE_API_KEY_P8_BASE64:-}" ]; then
	notice "macOS notarization skipped: APPLE_API_KEY_ID, APPLE_API_ISSUER_ID and APPLE_API_KEY_P8_BASE64 are not all set"
	exit 0
fi

printf '%s' "$APPLE_API_KEY_P8_BASE64" | decode >"$work/key.p8"
"$rcodesign" encode-app-store-connect-api-key \
	--output-path "$work/key.json" \
	"$APPLE_API_ISSUER_ID" "$APPLE_API_KEY_ID" "$work/key.p8" >/dev/null

# The notary service takes a zip. The ticket is keyed to each binary's code
# directory hash, so renaming the binary to tada when packaging keeps it.
(cd "$bin_dir" && zip -qX "$work/notarize.zip" tada_darwin_amd64 tada_darwin_arm64)
"$rcodesign" notary-submit \
	--api-key-file "$work/key.json" \
	--wait --max-wait-seconds 1800 \
	"$work/notarize.zip"
echo "Notarized ${bins[*]}"
