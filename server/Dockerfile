# The Brigade server's container image: the release binary on Debian's slim base.
#
#   docker build -t brigade-server server        (from the repository root; the context is server/)
#
# The builder carries the toolchain server/rust-toolchain.toml pins, and both stages are the same Debian
# release, so the binary finds the C library it was linked against. Cargo fetches Cavalry from GitHub at the
# commit the lock file names.
FROM rust:1.99.0-trixie AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/src/target \
    cargo build --release --locked -p brigade-server \
 && cp target/release/brigade-server /brigade-server

FROM debian:trixie-slim
LABEL org.opencontainers.image.title="brigade-server" \
      org.opencontainers.image.description="A backend for Brigade's REST contract on the file system and a Cavalry feed" \
      org.opencontainers.image.source="https://github.com/appshapes/brigade" \
      org.opencontainers.image.licenses="MIT"
# The server never runs as root. Its data directory is a volume the operator keeps: the log, the snapshots and
# the key the entries are sealed under.
RUN useradd --system --uid 10001 --home-dir /data --shell /usr/sbin/nologin brigade \
 && install -d -o brigade -g brigade -m 0700 /data
COPY --from=build /brigade-server /usr/local/bin/brigade-server
USER brigade
VOLUME /data
EXPOSE 8743
# The operator's arguments follow these. A container is reached from outside it, so they always name the
# listener and how TLS is served: `--listen 0.0.0.0:8743` with `--tls-cert` and `--tls-key`, or with
# `--tls-in-front`. Cavalry's driver directory is on /dev/shm, which Docker makes 64 MiB: `--shm-size=256m`.
ENTRYPOINT ["/usr/local/bin/brigade-server", "--data", "/data"]
