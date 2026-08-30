#!/bin/sh
# brigade: bootstrap wrapper shipped in the Claude Code plugin as plugin/bin/brigade.
# Resolves the release binary pinned by plugin/bin/VERSION, downloads and verifies it once against the
# committed plugin/bin/checksums.txt, caches it under the user's data directory, then execs it with every
# argument and stdin untouched. Afterwards every call is one stat() plus exec. POSIX sh only (dash-clean).
set -u

die() { printf 'brigade: %s\n' "$*" >&2; exit 9; }   # 9 = "unavailable" in the Brigade error taxonomy

# ---- where am I (follow symlinks, e.g. ~/.local/bin/brigade -> plugin/bin/brigade) ----------------------
script=$0
while [ -L "$script" ]; do
  link=$(readlink "$script") || break
  case $link in /*) script=$link ;; *) script=${script%/*}/$link ;; esac
done
case $script in */*) dir=${script%/*} ;; *) dir=. ;; esac
bindir=$(CDPATH= cd -- "$dir" 2>/dev/null && pwd -P) || die "cannot resolve the script directory"
plugin_root=${bindir%/*}

# ---- pinned version and checksums, both committed with the plugin -------------------------------------
[ -r "$bindir/VERSION" ] || die "missing $bindir/VERSION"
read -r version < "$bindir/VERSION"
case $version in ''|*[!0-9A-Za-z.+-]*) die "invalid version in $bindir/VERSION" ;; esac
checksums=$bindir/checksums.txt
[ -r "$checksums" ] || die "missing $checksums"

# ---- platform -----------------------------------------------------------------------------------------
sys=$(uname -sm 2>/dev/null) || die "uname failed"
os=${sys%% *}; arch=${sys##* }
case $os in Darwin) os=darwin ;; Linux) os=linux ;; *) die "unsupported OS '$os' (supported: macOS, Linux, WSL2)" ;; esac
case $arch in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) die "unsupported CPU architecture '$arch'" ;; esac

home=${HOME:-/nonexistent}

# ---- developer override: ONLY the user's own config dir, or a sibling build next to a --plugin-dir checkout.
# Never an environment variable (a trusted repository's settings `env` block can set those) and never a file
# inside a marketplace-installed plugin.
config_home=${XDG_CONFIG_HOME:-$home/.config}
if [ -r "$config_home/brigade/dev-binary" ]; then
  read -r dev < "$config_home/brigade/dev-binary"
  case $dev in /*) [ -x "$dev" ] && exec "$dev" "$@" ;; esac
  die "$config_home/brigade/dev-binary does not name an executable absolute path: '$dev'"
fi
claude_dir=${CLAUDE_CONFIG_DIR:-$home/.claude}
case $plugin_root in
  "$claude_dir"/plugins/*) ;;                              # marketplace cache: never consult sibling files
  *) [ -x "$plugin_root/../dist/brigade" ] && exec "$plugin_root/../dist/brigade" "$@" ;;   # `make build` output in a checkout
esac

# ---- cache: one per user, shared by hooks, the Bash tool and the human's terminal -----------------------
# (CLAUDE_PLUGIN_DATA is not exported to the Bash tool, verified on 2.1.251, so the cache cannot live there.)
data_home=${XDG_DATA_HOME:-$home/.local/share}
cache_dir=$data_home/brigade/bin
target=$cache_dir/brigade-$version-$os-$arch
[ -x "$target" ] && exec "$target" "$@"

# ---- first use: download, verify, install atomically, exec ----------------------------------------------
asset=brigade_${version}_${os}_${arch}.tar.gz
base=${BRIGADE_RELEASE_BASE_URL:-https://github.com/appshapes/brigade/releases/download}   # override is safe: sha256 is pinned below
url=$base/v$version/$asset
expected=$(awk -v f="$asset" '$2 == f { print $1; exit }' "$checksums")
[ -n "$expected" ] || die "no sha256 for $asset in $checksums: this plugin version ships no binary for $os/$arch"
command -v curl >/dev/null 2>&1 || die "curl is required to download $url; install curl, or download and verify it yourself (sha256 $expected) and extract 'brigade' to $target"
if command -v shasum >/dev/null 2>&1; then sha() { shasum -a 256 "$1" | awk '{print $1}'; }
elif command -v sha256sum >/dev/null 2>&1; then sha() { sha256sum "$1" | awk '{print $1}'; }
else die "shasum or sha256sum is required to verify the download"; fi

umask 077
mkdir -p "$cache_dir" || die "cannot create $cache_dir"
tmp=$(mktemp -d "$cache_dir/.tmp.XXXXXX") || die "cannot create a temporary directory in $cache_dir"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
printf 'brigade: first use: downloading brigade %s for %s/%s from %s\n' "$version" "$os" "$arch" "$base" >&2
if ! curl -fsSL --retry 3 --retry-delay 1 --retry-connrefused --connect-timeout 10 --max-time 180 -o "$tmp/$asset" "$url" </dev/null; then
  proxy=${HTTPS_PROXY:-${https_proxy:-}}
  [ -n "$proxy" ] && proxy=" (HTTPS_PROXY=$proxy)"
  die "download failed: $url$proxy. Check network or proxy access, or download the file yourself, verify sha256 $expected, and extract 'brigade' to $target"
fi
actual=$(sha "$tmp/$asset")
[ "$actual" = "$expected" ] || die "checksum mismatch for $asset: expected $expected, got $actual; refusing to install"
tar -xzf "$tmp/$asset" -C "$tmp" brigade </dev/null 2>/dev/null || die "cannot extract 'brigade' from $asset"
[ -f "$tmp/brigade" ] || die "$asset does not contain 'brigade'"
chmod 0755 "$tmp/brigade"
# macOS: curl sets no com.apple.quarantine attribute (verified), but strip one if a browser download left it
if [ "$os" = darwin ] && command -v xattr >/dev/null 2>&1; then xattr -d com.apple.quarantine "$tmp/brigade" 2>/dev/null || true; fi
mv -f "$tmp/brigade" "$target" || die "cannot install $target"       # rename(2): atomic; concurrent first runs are harmless
rm -rf "$tmp"; trap - EXIT HUP INT TERM
exec "$target" "$@"
