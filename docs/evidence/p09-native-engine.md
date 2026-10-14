# Selected native engine

New managed allocations select the published patched SpacetimeDB 2.11.0 image below.
Existing runtime records retain their own exact image, platform and configuration
identity. Changing this profile does not update an existing engine.

Image: `ghcr.io/shaullavo/brine-native-engine@sha256:d84c721e4efb07e153439f1e1e67e08569377d124017bf664c032aaa5b2f5715`.

The [successful main publication](https://github.com/ShaulLavo/brine/actions/runs/38517274196)
built both native Linux architectures from Brine source
`38a43eb8f94d2afd63d470b8da256bb68291abb5`. It verified the bundle checksums
and packaged the executables without recompilation. Anonymous registry readback
verified the index bytes, both platform manifests and their configuration blobs.

| Linux architecture | Platform manifest | Configuration ID | Native executable SHA-256 |
| --- | --- | --- | --- |
| amd64 | `sha256:749503c7980af9f9063e32dac02866579d5908249834b08a35842e2b3f8ab909` | `sha256:b493781d5f9d80e853c68b8dee7bf12fffa640d98b6c8e1fdc2469f15312ee60` | `b2a48b110d9d90f9baf5bd39092260c7e6b90c85c3c3efa7759b2b3a44b342e1` |
| arm64 | `sha256:bf2f2d6b8d6a48fae10d547378cc148ec25e2f85ffc840f5f14d37850108abe0` | `sha256:b26ed648341ee269193ff507fc2d2fba354c8b455f2b7277f55120723e8d3662` | `a03e803d8fb912aea76dd044f02dee0e298319457aec2319943084b3535a09a4` |

The upstream source remains
`6b6e0ab19e4f8f52472aa4379552e9593c8f53bb`, version 2.11.0.
The reviewed snapshot shutdown patch is
`c24ec6785f09b70b22a3fafa43eb034b6c7bdd816581c292bd49c96ece8c382e`.
It closes snapshot admission after durability and joins admitted compression work
before runtime shutdown. The image retains the original runtime base, user
`spacetime` and entrypoint `spacetime`. Brine runs the fixed native executable
`/opt/spacetime/spacetimedb-standalone` with UID/GID 1000 through its typed renderer.

The [native bundle recipe](../../tools/native-engine/README.md) binds the original
base, compiler, V8 archive and patch. Both native architecture publication gates
executed the packaged binary and verified the runtime ABI and exact version.
The [original runtime qualification](p09-runtime.md) and its
[official archive pins](../../tools/spacetime-runtime/pins.json) remain historical
evidence. They are not the selected image for new allocations.

This publication establishes artifact identity. It does not establish the full
managed Pi, AMD64, browser, S3 recovery, interruption or physical exit acceptance.
Those gates remain in the [phase plan](../plans/09-spacetimedb.md).
