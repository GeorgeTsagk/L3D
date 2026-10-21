# Source provenance

L3D packages a private regtest client for LayerTree. The design, tree format,
reference script generators, transaction builders, wire messages and test
vectors originate in [lightninglabs/layertree](https://github.com/lightninglabs/layertree).

The reference baseline is
[`f64cc0e4d4471e32f7fd8294ecb0b1c7547c1166`](https://github.com/lightninglabs/layertree/tree/f64cc0e4d4471e32f7fd8294ecb0b1c7547c1166).
The `tree`, `script`, `txn`, `msg` and `spec` directories retain that reference
implementation and its tests. Go import paths are adjusted to this module.
The spec directory contains the two test-vector files. Source comments that
refer to Python tools or specification sections refer to the upstream tree.

Local additions are `lite`, `operator`, `protocol`, `mvp`, the member
transaction builder, native-interpreter tests and command-line programs.
The Makefile, configuration examples and backend patch make those additions
reproducible from a fresh checkout.

The backend starts from btcd
[`1c55c7c18179f9638f5a481151692516c9db45d6`](https://github.com/btcsuite/btcd/tree/1c55c7c18179f9638f5a481151692516c9db45d6).
`patches/btcd-layertree.patch` adds the experimental interpreter, transaction
validation and regtest activation gate. `scripts/backend.env` pins both the
base commit and the expected patched Git tree. Its downloaded source retains
the upstream btcd license and copyright notices.

The protocol code is a snapshot. Updating the research baseline or opcode
assumptions requires updating the scripts, vectors and backend together.
The website may describe research changes that are newer than this snapshot.
