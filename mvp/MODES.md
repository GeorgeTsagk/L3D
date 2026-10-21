# Selectable LayerTree modes

**Private regtest testing only.** Commands run from the repository root.
See [setup and configuration](../README.md) and the author's
[tier guide](https://layertree.tech/components/tiers.html).

The same btcd backend validates all of the modes below. Each tree is created
with one profile and one opcode tier. Its root and transition scripts bind
that choice. Choosing another mode means joining another tree.

## Components

1. `protocol`: public deployment parameters, supported combinations, numeric
   limits and conversion to the author's script option types.
2. `tree`, `script`, `txn`: the author's tree and covenant machinery, plus a
   member builder that uses one authenticated path instead of the full tree.
3. `operator`: a coordinator holding the complete tree and its own signing
   key. It reconstructs member requests before adding its signature. It
   never needs the member's private key.
4. `lite`: keys, one leaf and its path, transaction preparation and recovery
   from confirmed tree spends. Each CLI command starts and stops.
5. Patched btcd: native execution and validation in mempool and blocks.

The coordinator is a Go library exercised by the demonstration. Requests
and responses use local proposal files. There is no network operator service
or discovery protocol in this MVP. A real operator would expose admission,
proof delivery, signing and batch coordination through its own API.

## Supported configurations

| CLI tier | Numerics | Profiles | Member commands |
| --- | --- | --- | --- |
| `full` | C2 unsigned | dense, sparse | join, update, exit |
| `mid` | C0 Script numbers | dense, sparse | join, update, exit |
| `min` | C0 Script numbers | dense | attach funded membership, exit |
| `ccv` | C2 unsigned | dense, sparse | join, update, exit |
| `ccv0` | C0 Script numbers | dense, sparse | join, update, exit |

These are nine combinations of four tiers. CCV has two numeric variants.
Sparse MIN is not implemented by the reference. MIN has a fixed EXIT and
SPLIT script set, with no JOIN, UPDATE or cohort rewrite. The member CLI does
not expose SPLIT or the unsupported MIN terminal exit.

C0 trees hold at most 2,147,483,647 sats. Their sparse profile requires depth
25 through 31 and a usable nonce. FULL and CCV accept depths through 63 in
this client. Every accepted tree has an owner-authorized EXIT_1. This MVP
excludes optional reclaim, detach, tower and exit-consent configurations.

Dense modes support cohort grafts and rewrites through the transaction
library. C0 rewrites retain the reference's 1,000-element stack bound. The
CLI prepares individual member transactions and follows cohort transitions
while syncing. It does not collect member signatures for a batch.

## Operator responsibilities

The operator chooses a tree's creation parameters, allocates dense positions,
checks sparse vacancies, delivers join-time proofs, and signs joins and
cooperative updates. It can organize multiple independent trees of different
types on the same backend. A client checks its offered type against the
on-chain output, then stores those parameters with its proof.

The operator's signature cannot spend another member's leaf by itself.
Members authorize their updates and cooperative exits. A unilateral exit
uses the owner's signature. An eviction after the lease pays the leaf's
committed exit key. Offline clients recover proofs from confirmed witnesses.

## Run

```sh
make demo RUN_DIR=/tmp/l3d-modes
```

This starts one private regtest node and creates nine trees. Every client
command is a fresh process. Each ordinary tree exercises admission, increase,
decrease, other-member activity while the client is offline, then an exit
without operator signing. Dense ordinary trees also exercise four-user joins,
updates and exits using cohort scripts. MIN members are funded at creation.

`catalog.json` contains public tree descriptors. Each mode directory holds
an offer, member state, proposal files, raw transactions and an operator
snapshot for every confirmed transition. `modes-report.json` records the
actual serialized transaction sizes and results. The harness checks that
member files remain unchanged while no member process runs.

The node rejects freshly signed incorrect remainders in every ordinary mode.
It also rejects CCV transactions that underfund a shared output or deduct the
same output twice across different inputs. A correctly accumulated shared
output is mined.

```sh
./bin/l3d modes
./bin/l3d init -state alice.json
./bin/l3d attach -state alice.json -rpc-config btcd.conf -proof offer.json -tier ccv -profile dense
./bin/l3d status -state alice.json
```

The normal join, update, exit, submit and sync commands work unchanged.
Use a separate state file for each membership. An operator offer must refer
to a current confirmed output. The demonstration catalog records creation
outpoints, so it is a transcript, not a live registry.

## Backend boundaries

Activation remains restricted to private regtest with the experimental
LayerTree flag. The backend uses the author's placeholder opcode numbers.
It implements the opcode subset exercised by the four tiers, including
C2 unsigned arithmetic, restored slicing and bit operations, EC point
operations and CCV. OP_TX retains the four selectors the ordinary tree
scripts use. Channel leaves and the hypothetical Merkle-sum opcode are
outside scope.

The C2 interpreter uses bounded element and stack sizes and an experimental
work budget. This is not a complete consensus implementation of BIP440 or
BIP441. Its work charges are conservative estimates, not the exact proposed
schedule. CCV accounting follows the pinned spec assumptions, including a
shared transaction context and ordered input execution.

The pinned CCV0 scripts use a raw one-byte push for the -1 sentinel. The
experimental flag permits that specific push despite ordinary minimal-push
policy. Minimal C0 numeric operand checks remain enabled. No such exception
is enabled on an ordinary node or without the experimental flag.

The client trusts local btcd RPC for chain selection and UTXOs. It does not
implement SPV. Reorganizations across its saved checkpoint still require an
earlier backup. Keys are unencrypted MVP state files with mode 0600.

## Verification

The Go suite executes all 427 upstream transaction scenarios through the
native btcd interpreter, including four negative reference witnesses. New
member tests compare serialized transactions with the reference builders
in every supported combination and recover proofs from missed spends.

The regtest suite runs the original lifecycle, the original offline client
scenario, and the nine-mode demonstration. It retains the original native
block-rejection test and adds the shared-input CCV accounting checks.

```sh
make check
```
