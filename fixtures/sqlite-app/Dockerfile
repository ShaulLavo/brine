# scratch is the empty filesystem, not a mutable registry image/tag.
# Workflow cross-compiles the single static binary before assembling the image.
FROM scratch
ARG TARGETARCH
LABEL org.opencontainers.image.source="https://github.com/ShaulLavo/brine"
COPY bin/${TARGETARCH}/sqlite-app /sqlite-app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/sqlite-app"]
