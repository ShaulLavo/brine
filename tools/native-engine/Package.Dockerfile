FROM docker.io/clockworklabs/spacetime@sha256:088aeefda7397bc265876e27fa254cbff64b8563dcc8d28db98423176c64af63
ARG TARGETARCH
COPY --chmod=0555 ${TARGETARCH}/spacetimedb-standalone /opt/spacetime/spacetimedb-standalone
COPY ${TARGETARCH}/ /usr/share/brine-native-engine/
