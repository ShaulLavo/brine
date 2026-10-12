FROM docker.io/library/rust@sha256:14b9b5f47dcc6644d0f0c1b35a2c2c5d0124f67159aaee28a348627523459b55 AS build
ARG TARGETARCH
ENV SOURCE_DATE_EPOCH=0 CARGO_INCREMENTAL=0
WORKDIR /build
COPY SpacetimeDB-6b6e0ab19e4f8f52472aa4379552e9593c8f53bb /build
RUN case "$TARGETARCH" in amd64) target=x86_64-unknown-linux-musl ;; arm64) target=aarch64-unknown-linux-musl ;; *) exit 64 ;; esac \
    && test "$(rustc --version | cut -d ' ' -f 2)" = 1.96.1 \
    && RUSTFLAGS='-C target-feature=+crt-static --remap-path-prefix=/build=/source' cargo build --locked --release --target "$target" -p brine-native-reader -j 2 \
    && mkdir /artifact \
    && RUSTFLAGS='-C target-feature=+crt-static --remap-path-prefix=/build=/source' cargo test --locked --target "$target" -p brine-native-reader -j 2 --no-run \
    && cp "target/$target/release/brine-native-reader" /artifact/brine-native-reader \
    && strip /artifact/brine-native-reader \
    && ! readelf -l /artifact/brine-native-reader | grep -q INTERP \
    && (cd /artifact && sha256sum brine-native-reader > SHA256SUMS)
COPY pins.json upstream-LICENSE.txt /artifact/

FROM build AS native-tests
ARG TARGETARCH
RUN case "$TARGETARCH" in amd64) target=x86_64-unknown-linux-musl ;; arm64) target=aarch64-unknown-linux-musl ;; *) exit 64 ;; esac \
    && BRINE_DISCOVERY_READER_TEST_OUTPUT=/artifact/discovery-negative-fixtures BRINE_READER_TEST_OUTPUT=/artifact/fixture BRINE_ZERO_READER_TEST_OUTPUT=/artifact/replica-zero-fixture BRINE_PROJECTION_READER_TEST_OUTPUT=/artifact/projection-fixtures RUSTFLAGS='-C target-feature=+crt-static --remap-path-prefix=/build=/source' cargo test --locked --target "$target" -p brine-native-reader -j 2 -- --test-threads=1

FROM scratch AS artifact
COPY --from=native-tests /artifact/ /

FROM scratch AS runtime
COPY --from=native-tests /artifact/ /usr/share/brine-native-reader/
COPY --from=native-tests /artifact/brine-native-reader /brine-native-reader
ENTRYPOINT ["/brine-native-reader"]
