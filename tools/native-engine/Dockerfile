FROM docker.io/library/rust@sha256:a339861ae23e9abb272cea45dfafde21760d2ce6577a70f8a926153677902663 AS build
ARG TARGETARCH
ENV SOURCE_DATE_EPOCH=0 CARGO_INCREMENTAL=0
WORKDIR /build
COPY SpacetimeDB-6b6e0ab19e4f8f52472aa4379552e9593c8f53bb /build
COPY pins.json graceful-shutdown.patch upstream-LICENSE.txt /artifact/
RUN test "$(rustc --version | cut -d ' ' -f 2)" = 1.96.1 \
    && case "$TARGETARCH" in \
         amd64) target=x86_64-unknown-linux-gnu; v8_sha=7215753c0c78d141f752d7b993794bae07e18a1dfd466dcaa84fa64e76bacac1 ;; \
         arm64) target=aarch64-unknown-linux-gnu; v8_sha=e088af62c921512b0c2d963defe836dd4b54621e29e6393b0b384c6cccaa5f26 ;; \
         *) exit 64 ;; \
       esac \
    && curl --fail --location --max-time 180 --output /v8.a.gz "https://github.com/denoland/rusty_v8/releases/download/v145.0.0/librusty_v8_release_$target.a.gz" \
    && printf '%s  /v8.a.gz\n' "$v8_sha" | sha256sum --check \
    && RUSTY_V8_ARCHIVE=/v8.a.gz cargo build --locked --release -p spacetimedb-standalone -j 2 \
    && cp target/release/spacetimedb-standalone /artifact/spacetimedb-standalone \
    && chmod 0555 /artifact/spacetimedb-standalone \
    && cp Cargo.lock /artifact/Cargo.lock \
    && (cd /artifact && sha256sum spacetimedb-standalone pins.json graceful-shutdown.patch upstream-LICENSE.txt Cargo.lock > SHA256SUMS)

FROM scratch AS artifact
COPY --from=build /artifact/ /

FROM docker.io/clockworklabs/spacetime@sha256:088aeefda7397bc265876e27fa254cbff64b8563dcc8d28db98423176c64af63 AS runtime
COPY --from=build --chmod=0555 /artifact/spacetimedb-standalone /opt/spacetime/spacetimedb-standalone
COPY --from=build /artifact/ /usr/share/brine-native-engine/
