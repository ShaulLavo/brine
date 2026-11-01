FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
ARG TARGETOS
ARG TARGETARCH
ARG RELEASE=udp-echo-v1
WORKDIR /src
COPY go.mod go.sum ./
COPY fixtures/udp-echo ./fixtures/udp-echo
ENV GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=0
RUN test "$TARGETOS" = linux && case "$TARGETARCH" in amd64|arm64) ;; *) exit 1 ;; esac \
    && test -n "$RELEASE" && test "${#RELEASE}" -le 64 && case "$RELEASE" in *[!a-zA-Z0-9._-]*) exit 1 ;; esac \
    && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.release=$RELEASE" -o /out/udp-echo-server ./fixtures/udp-echo/server/cmd \
    && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/udp-echo-client ./fixtures/udp-echo/client/cmd

FROM scratch AS udp-echo-client
COPY --from=build --chmod=0555 /out/udp-echo-client /udp-echo-client
USER 65532:65532
ENTRYPOINT ["/udp-echo-client"]

FROM scratch AS udp-echo-server
COPY --from=build --chmod=0555 /out/udp-echo-server /udp-echo-server
USER 65532:65532
EXPOSE 8080/tcp 7777/udp
ENTRYPOINT ["/udp-echo-server"]
