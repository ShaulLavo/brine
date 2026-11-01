# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26.0@sha256:fb612b7831d53a89cbc0aaa7855b69ad7b0caf603715860cf538df854d047b84 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY fixtures/udp-echo ./fixtures/udp-echo
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/udp-echo-server ./fixtures/udp-echo/server/cmd
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/udp-echo-client ./fixtures/udp-echo/client/cmd

FROM scratch AS udp-echo-server
COPY --from=build /out/udp-echo-server /udp-echo-server
USER 65532:65532
EXPOSE 8080/tcp 7777/udp
ENTRYPOINT ["/udp-echo-server"]

FROM scratch AS udp-echo-client
COPY --from=build /out/udp-echo-client /udp-echo-client
USER 65532:65532
ENTRYPOINT ["/udp-echo-client"]

FROM udp-echo-server
