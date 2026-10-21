# L3D

**For private regtest testing only. Do not use with real funds.**

A lightweight Go client for [LayerTree](https://layertree.tech/), with a patched
btcd backend that executes its experimental covenant scripts. Member commands
run on demand and exit. Members keep their own keys, leaf and Merkle path.

The protocol comes from [lightninglabs/layertree](https://github.com/lightninglabs/layertree).
This repository adds the member client, operator coordinator, native regtest
backend and demonstrations. It pins the reference code and backend rather
than following their moving branches. See [source provenance](UPSTREAM.md).

## Quick start

Requirements: Linux or macOS, Go 1.23.6 or newer, Git, GNU Make and Bash.
The first build downloads the pinned btcd source and Go dependencies.
The race target also requires a C compiler.

```sh
git clone https://github.com/GeorgeTsagk/L3D.git
cd L3D
make build
make demo
```

`make demo` starts one isolated regtest node, creates nine trees, runs the
member clients, mines their transactions, and stops the node. It exercises
joins, balance increases and decreases, offline proof recovery and unilateral
exits. Dense modes also exercise four-member cohort batches. MIN members
are funded at creation because that tier has no JOIN or UPDATE.

Results go into a new `runs/` directory. `modes-report.json` records transaction
sizes and outcomes. `catalog.json` records tree descriptions. The other files
contain disposable wallets, proofs and raw transactions. Each run owns its
own node and data directory. It does not use an already running node.

```sh
make demo RUN_DIR=/tmp/l3d-demo
make modes
make check
```

`RUN_DIR` must be empty or absent. The demo refuses to overwrite an earlier run.

## Supported modes

A tree fixes its opcode tier and shape at creation. The member checks that
the operator's offer matches the selected mode and the confirmed tree output.
Use separate wallet files to join different trees.

| `tier` | Numerics | `profile` | Member operations |
| --- | --- | --- | --- |
| `full` | C2 unsigned | `dense`, `sparse` | Join, update, exit |
| `mid` | C0 Script numbers | `dense`, `sparse` | Join, update, exit |
| `min` | C0 Script numbers | `dense` | Attach funded leaf, exit |
| `ccv` | C2 unsigned | `dense`, `sparse` | Join, update, exit |
| `ccv0` | C0 Script numbers | `dense`, `sparse` | Join, update, exit |

These are nine configurations of four tiers. CCV has two numeric variants.
Dense trees use operator-allocated positions and support cohort grafts and
rewrites. Sparse trees derive candidate positions from the leaf key and nonce.
C0 trees are limited to 2,147,483,647 sats in total. C0 sparse depths are 25
through 31. FULL and CCV support depths through 63 in this client.

The transaction and operator libraries assemble dense batches. The member CLI
signs individual transactions and recovers its proof across confirmed batches.
Collecting batch signatures through the CLI is not implemented.

See [the mode guide](mvp/MODES.md), the author's
[tier explanation](https://layertree.tech/components/tiers.html),
[tree profiles](https://github.com/lightninglabs/layertree/blob/f64cc0e4d4471e32f7fd8294ecb0b1c7547c1166/design/profiles.md)
and [batching explanation](https://layertree.tech/components/batching.html).

## Configuration

The demo configures its own node and wallets. For a manually managed node:

```sh
cp sample-btcd.conf btcd.conf
cp sample-l3d.json l3d.json
./bin/l3d init -config l3d.json
make run-backend
```

`make run-backend` stays in the foreground. Use another terminal for client
commands. The sample node exposes RPC only on `127.0.0.1:18443`, disables peer
connections and enables `txindex` and `experimental-layertree`. Use a separate
data directory from any other Bitcoin node. The backend refuses experimental
activation on mainnet, testnet and simnet.

Edit `l3d.json` to choose a mode:

```json
{
  "network": "regtest",
  "state": "data/alice.json",
  "rpc_config": "btcd.conf",
  "tier": "ccv",
  "profile": "dense",
  "depth": 8,
  "fee_sat": 20000
}
```

- `state` is the wallet file. It contains an unencrypted private key and proof.
- `rpc_config` points to the local btcd configuration.
- `tier` and `profile` pin the type accepted by `attach`.
- `depth` is used by `locate` for sparse candidate allocation. It does not
  change a tree's committed depth.
- `fee_sat` is an absolute transaction fee. This MVP does not estimate fees.
- `network` must be `regtest`.

Paths in the JSON are relative to that file. Explicit command flags override
the JSON values. Paths supplied as flags are relative to the working directory.
Unknown JSON fields and unsupported settings are rejected. Nothing loads
`l3d.json` implicitly: pass `-config` to each command.

The node configuration and client configuration select different things.
One node validates every supported mode. The operator creates a particular
tree and supplies an offer containing its parameters and authenticated path.
Changing the JSON does not create a tree or change an existing membership.

## Using the client

```sh
./bin/l3d modes
./bin/l3d status -config l3d.json
./bin/l3d attach -config l3d.json -proof offer.json
./bin/l3d join -config l3d.json -amount 100000 -evict-after 100000 -funding TXID:VOUT -out join.json
```

The operator supplies `offer.json`, checks the join proposal and adds its
signature. Submit the signed proposal, mine it, then synchronize:

```sh
./bin/l3d submit -config l3d.json -request join-signed.json
./bin/l3d sync -config l3d.json
./bin/l3d update -config l3d.json -amount 150000 -evict-after 100000 -funding TXID:VOUT -out update.json
```

`update` also needs the operator's signature before submission. `amount` is
the new total balance. Funding inputs must belong to the funding address
printed by `init`. The node needs mined regtest funds and an operator-created
tree. `make demo` provides that complete setup automatically.

An individual exit needs no operator signature:

```sh
./bin/l3d exit -config l3d.json -funding FEE_TXID:VOUT -out exit.json
./bin/l3d submit -config l3d.json -request exit.json
./bin/l3d sync -config l3d.json
```

Spend commands synchronize first. Offline commands include `init`, `status`,
`locate`, `modes` and `version`. Use `./bin/l3d join -h` for flags and the
[client guide](lite/README.md) for proof storage, sparse allocation and recovery.

## Operator

The operator holds the full tree, allocates dense positions, checks sparse
vacancies, provides initial proofs, cosigns cooperative changes and coordinates
batches. Member private keys stay with members. Exits use the member's stored
proof and data recovered from confirmed transactions.

This MVP has an [operator Go library](operator/operator.go), exercised by the
demo through local proposal files. It has no network operator service,
discovery API or standalone operator command. Manual integrations supply that
coordination themselves.

## Development

The Makefile follows the build, unit, integration and formatting target
conventions used by lnd and taproot-assets. It requires no Docker setup.

```sh
make help
make build
make install PREFIX="$HOME/.local"
make unit
make unit PKGS=./lite CASE=Test
make unit-race
make backend-test
make itest ITEST=TestRegtestAllModes
make lint
make check
```

`make check` runs formatting checks, `go vet`, unit tests, native-script tests,
the changed btcd package tests and three real-node scenarios. The native
interpreter tests cover all 427 pinned upstream transaction cases. Integration
tests reject malformed remainders and invalid CCV accounting across inputs.
CI runs `make check` and `make unit-race`.

`make deps` fetches the exact backend commit in `scripts/backend.env`, applies
`patches/btcd-layertree.patch` and checks the resulting Git tree. `go.work`
selects this backend for both the client and node. Stock btcd cannot build
or run the experimental client. `GOTOOLCHAIN=local` is the default.

`make clean` removes binaries. It preserves wallets and chain data.
`make clean-deps` removes the downloaded backend. Save any local backend edits
before using it. Builds refuse a modified backend instead of overwriting it.

## MVP boundaries

- Only private regtest is supported. Opcode numbers and consensus behavior
  are experimental. C2 work accounting is not a complete BIP440 implementation.
- The client trusts local indexed btcd RPC. It does not implement SPV.
- A reorg across the stored checkpoint needs an earlier valid wallet backup.
- Back up the complete wallet file. A private key alone does not replace
  the initial leaf and Merkle proof.
- MIN has no member JOIN, UPDATE, batch rewrite or final-leaf terminal exit.
- Channel leaves, optional reclaim, detach, tower and exit-consent modes are
  outside the member client. The CLI also has no voucher or CLAIM command.
- There is no automatic coin selection, fee estimation or wallet encryption.

## LayerTree resources

- [Website](https://layertree.tech/)
- [Research and reference implementation](https://github.com/lightninglabs/layertree)
- [Pinned specification](https://github.com/lightninglabs/layertree/blob/f64cc0e4d4471e32f7fd8294ecb0b1c7547c1166/spec/layertree.md)
- [Paper](https://github.com/lightninglabs/layertree/blob/f64cc0e4d4471e32f7fd8294ecb0b1c7547c1166/paper/main.pdf)
- [Join](https://layertree.tech/components/join.html),
  [exit](https://layertree.tech/components/exit.html) and
  [batching](https://layertree.tech/components/batching.html)
- [Cost comparison](https://github.com/lightninglabs/layertree/blob/f64cc0e4d4471e32f7fd8294ecb0b1c7547c1166/design/cost-comparison.md)
