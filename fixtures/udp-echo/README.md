# UDP echo fixture

This disposable, non-root fixture serves bounded HTTP health and a versioned UDP nonce echo for Phase 08 deployment evidence. The client is a separate bounded probe that prints one JSON result with its endpoint, network vantage, attempts, timestamps and response identity.

Build the server image from the repository root with a digest-pinned Go builder:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -f fixtures/udp-echo/Dockerfile -t ghcr.io/shaullavo/brine-udp-echo:<reviewed-tag> --push .
```

Use the published immutable index and selected platform manifest digests in any disposable app specification. The image defaults to HTTP `:8080` and UDP `:7777`, runs as UID/GID `65532:65532`, and accepts `BRINE_UDP_ECHO_RELEASE` to identify each release. The client image is available with `--target udp-echo-client`; local tests use the Go packages directly.

The fixture does not implement QUIC, WebTransport, TLS, discovery, authentication or a proxy. It is evidence for direct HTTP health and UDP ownership/reachability only.
