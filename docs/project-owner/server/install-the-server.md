# Install the server

`brigade-server` is one program with no database and no other service beside it. It is released apart from the
plugin, with its own version and its own tags, `server-v<version>`.

## What runs it

| Machine | |
| --- | --- |
| Linux, x86-64 or ARM (aarch64) | glibc 2.36 or later: Debian 12, Ubuntu 24.04 and later |
| macOS, Apple silicon | |
| Windows | not supported: the log the server keeps, [Cavalry](https://github.com/appshapes/cavalry), runs on Linux and macOS |

This is the server's machine only. A member's machine runs the plugin and needs nothing of this, Windows included.

## The release binary

```sh
v=0.1.0
base=https://github.com/appshapes/brigade/releases/download/server-v$v
curl -fLO $base/brigade-server_${v}_linux_amd64
curl -fLO $base/checksums.txt
sha256sum --check --ignore-missing checksums.txt
sudo install -m 0755 brigade-server_${v}_linux_amd64 /usr/local/bin/brigade-server
brigade-server --version
```

The other two names are `brigade-server_<version>_linux_arm64` and `brigade-server_<version>_darwin_arm64`. On
macOS, `shasum -a 256 --check --ignore-missing checksums.txt` is the check.

## By crate

```sh
cargo install --git https://github.com/appshapes/brigade --tag server-v0.1.0 brigade-server --locked
```

Cargo builds the tagged source with the versions its lock file names and puts `brigade-server` in `~/.cargo/bin`.
It needs Rust 1.89 or later, `git` and a C compiler. `--locked` matters: without it Cargo picks newer versions
than the release was tested with. `cargo install brigade-server` from crates.io does not work yet: crates.io takes
no crate that depends on a git repository, and Cavalry is not published there before its first release.

## The image

```sh
docker pull ghcr.io/appshapes/brigade-server:0.1.0
```

The image holds the same program on Debian's slim base, runs as a user that is not root and keeps everything in
the volume `/data`. [Run the server](run-the-server.md#as-a-container) has the `docker run` line.

## From a clone

`make server-build` in a clone of the repository builds `server/target/release/brigade-server` with the pinned
toolchain. [Change the server](../../developer/change/server.md) is the developer's page.
