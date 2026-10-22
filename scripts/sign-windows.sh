#!/usr/bin/env bash
# Authenticode-signs Windows executables in place with osslsigncode (SHA-256,
# RFC 3161 timestamp). Runs on Linux or macOS. Used by
# .github/workflows/release.yml to sign tada.exe before it is zipped. The
# Setup.exe installers are signed on Windows by windows-installer.yml.
#
#   scripts/sign-windows.sh FILE.exe...
#
# Settings come from the environment (see docs/RELEASING.md):
#
#   WINDOWS_CERT_PFX_BASE64  Code-signing cert + key, .pfx, base64
#   WINDOWS_CERT_PASSWORD    Password of that .pfx
#   WINDOWS_TIMESTAMP_URL    RFC 3161 server (default: DigiCert's)
#
# No certificate: does nothing.
set -euo pipefail

[ $# -gt 0 ] || { echo "usage: $0 FILE.exe..." >&2; exit 2; }
ts_url=${WINDOWS_TIMESTAMP_URL:-http://timestamp.digicert.com}

notice() {
	if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::notice::$*"; else echo "$*"; fi
}
warn() {
	if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::warning::$*"; else echo "warning: $*" >&2; fi
}

if [ -z "${WINDOWS_CERT_PFX_BASE64:-}" ]; then
	notice "Windows signing skipped: WINDOWS_CERT_PFX_BASE64 is not set"
	exit 0
fi

for f in "$@"; do
	[ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
done

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf '%s' "$WINDOWS_CERT_PFX_BASE64" | tr -d ' \r\n' | base64 -d >"$work/cert.pfx"
printf '%s' "${WINDOWS_CERT_PASSWORD:-}" >"$work/cert.pass"

for f in "$@"; do
	osslsigncode sign \
		-pkcs12 "$work/cert.pfx" \
		-readpass "$work/cert.pass" \
		-h sha256 \
		-n "Ta-Da" \
		-i "https://tada.cetla.dev/" \
		-ts "$ts_url" \
		-in "$f" -out "$work/signed.exe"
	# cp keeps the file's mode, unlike mv from the 0600 temp file.
	cp "$work/signed.exe" "$f"
	# The runner's CA bundle may lack the code-signing root, so a failed
	# chain check is a warning, not an error. A missing signature still
	# fails the sign command above.
	osslsigncode verify -in "$f" || warn "osslsigncode verify could not confirm the chain for $f"
done
echo "Signed $*"
