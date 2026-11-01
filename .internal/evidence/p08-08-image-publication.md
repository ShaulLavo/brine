# P08-08 fixture publication preparation

Status is source preparation in progress. No image has been published by this change.

This draft claims only the new UDP fixture workflow, its typed publication helper,
focused tests under `tools/ci/udp-fixture/`, and this evidence file. It starts from
reviewed fixture merge `369744357524dc81019afc334a2f9376cf8c87b9`.

The publication contract binds a manually supplied reviewed main SHA to the run's
exact main checkout. Native amd64 and arm64 jobs qualify two compiled release
identities before a separate publication job consumes their retained archives.
Existing public package access and anonymous digest pulls remain hard gates.
No visibility change, custom credential, dispatch, image write or host effect is
part of source preparation. The parent owns independent review and later execution.
