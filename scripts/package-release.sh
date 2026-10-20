#!/usr/bin/env bash
# Packages prebuilt binaries into the release files: one archive per target
# (tar.gz, or zip for Windows) plus SHA256SUMS.txt. Used by
# .github/workflows/release.yml after the per-target build jobs.
#
#   scripts/package-release.sh VERSION BIN_DIR OUT_DIR
#
# BIN_DIR holds tada_<goos>_<goarch> (tada_windows_<goarch>.exe for Windows).
# Each archive holds one top-level directory with the binary, README.md,
# LICENSE.md, manpages/ and completions/, the same layout goreleaser made.
# Completions and the man page come from whichever binary runs on this
# machine.
set -euo pipefail

version=${1:?version}
bin_dir=$(cd "${2:?bin dir}" && pwd)
out_dir=${3:?out dir}
repo=$(cd "$(dirname "$0")/.." && pwd)
targets="darwin_amd64 darwin_arm64 linux_amd64 linux_arm64 windows_amd64 windows_arm64"

case "$(uname -s)_$(uname -m)" in
Linux_x86_64) host=linux_amd64 ;;
Linux_aarch64 | Linux_arm64) host=linux_arm64 ;;
Darwin_x86_64) host=darwin_amd64 ;;
Darwin_arm64) host=darwin_arm64 ;;
*) echo "unsupported host $(uname -sm)" >&2; exit 1 ;;
esac

mkdir -p "$out_dir"
out_dir=$(cd "$out_dir" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for t in $targets; do
	exe=tada_$t
	[[ $t == windows_* ]] && exe=$exe.exe
	[ -f "$bin_dir/$exe" ] || { echo "missing $bin_dir/$exe" >&2; exit 1; }
	chmod +x "$bin_dir/$exe" # artifact downloads drop the executable bit
done

hostbin="$bin_dir/tada_$host"
mkdir -p "$work/extra/completions" "$work/extra/manpages"
for sh in bash zsh fish; do
	"$hostbin" completion "$sh" >"$work/extra/completions/tada.$sh"
done
"$hostbin" man | gzip -9 -n >"$work/extra/manpages/tada.1.gz"

export COPYFILE_DISABLE=1 # no macOS ._ files when testing locally
for t in $targets; do
	goos=${t%_*}
	goarch=${t#*_}
	case $goos in darwin) os=Darwin ;; linux) os=Linux ;; windows) os=Windows ;; esac
	case $goarch in amd64) arch=x86_64 ;; *) arch=$goarch ;; esac
	name=tada_${version}_${os}_${arch}
	dir="$work/$name"
	mkdir -p "$dir"
	cp "$repo/README.md" "$repo/LICENSE.md" "$dir/"
	cp -R "$work/extra/completions" "$work/extra/manpages" "$dir/"
	if [ "$goos" = windows ]; then
		cp "$bin_dir/tada_$t.exe" "$dir/tada.exe"
		(cd "$work" && zip -qrX "$out_dir/$name.zip" "$name")
	else
		cp "$bin_dir/tada_$t" "$dir/tada"
		(cd "$work" && tar -czf "$out_dir/$name.tar.gz" "$name")
	fi
done

cd "$out_dir"
if command -v sha256sum >/dev/null; then
	sha256sum tada_* | sort -k2 >SHA256SUMS.txt
else
	shasum -a 256 tada_* | sort -k2 >SHA256SUMS.txt
fi
cat SHA256SUMS.txt
