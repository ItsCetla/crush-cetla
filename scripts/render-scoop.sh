#!/usr/bin/env bash
# Prints the Scoop manifest (tada.json) for a release, with the zip hashes
# taken from its SHA256SUMS.txt. Used by
# .github/workflows/package-managers.yml, which pushes the result to the
# bucket repository as bucket/tada.json. The checkver and autoupdate blocks
# also let Scoop's own tooling bump it between our pushes.
#
#   scripts/render-scoop.sh VERSION SHA256SUMS.txt [OWNER/REPO] >tada.json
#
# OWNER/REPO is the GitHub repository with the releases (default
# ItsCetla/tada).
set -euo pipefail

version=${1:?version}
sums=${2:?SHA256SUMS.txt}
repo=${3:-ItsCetla/tada}
version=${version#v}
base="https://github.com/$repo/releases/download/v$version"

sha() {
	local name="tada_${version}_$1.zip" h
	h=$(awk -v f="$name" '$2 == f || $2 == "*" f { print $1; exit }' "$sums")
	[[ $h =~ ^[0-9a-f]{64}$ ]] || { echo "no sha256 for $name in $sums" >&2; exit 1; }
	echo "$h"
}

amd64=$(sha Windows_x86_64)
arm64=$(sha Windows_arm64)

# $version, $baseurl and $basename inside the autoupdate block are Scoop's
# own variables, so they stay literal here.
cat <<EOF
{
    "version": "$version",
    "description": "Terminal coding agent that talks only to the model providers and MCP servers you configure.",
    "homepage": "https://tada.cetla.dev/",
    "license": "FSL-1.1-MIT",
    "architecture": {
        "64bit": {
            "url": "$base/tada_${version}_Windows_x86_64.zip",
            "hash": "$amd64",
            "extract_dir": "tada_${version}_Windows_x86_64"
        },
        "arm64": {
            "url": "$base/tada_${version}_Windows_arm64.zip",
            "hash": "$arm64",
            "extract_dir": "tada_${version}_Windows_arm64"
        }
    },
    "bin": "tada.exe",
    "checkver": {
        "github": "https://github.com/$repo"
    },
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/$repo/releases/download/v\$version/tada_\$version_Windows_x86_64.zip",
                "extract_dir": "tada_\$version_Windows_x86_64"
            },
            "arm64": {
                "url": "https://github.com/$repo/releases/download/v\$version/tada_\$version_Windows_arm64.zip",
                "extract_dir": "tada_\$version_Windows_arm64"
            }
        },
        "hash": {
            "url": "\$baseurl/SHA256SUMS.txt"
        }
    }
}
EOF
