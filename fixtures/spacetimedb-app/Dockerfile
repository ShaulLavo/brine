FROM scratch
ARG TARGETARCH
LABEL org.opencontainers.image.source="https://github.com/ShaulLavo/brine"
COPY bin/${TARGETARCH}/spacetimedb-app /spacetimedb-app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/spacetimedb-app"]
