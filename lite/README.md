# On-demand LayerTree client

**Private regtest testing only.** See the [setup guide](../README.md) for
configuration, supported modes and build targets. All examples run from
the repository root. Each command also accepts `-config l3d.json`.

This Go client runs one command and exits. It has no listener, background
watcher or continuously running member process. `status` reads local state
without contacting a node. Commands that spend first synchronize with the
chain.

The distinction is between the member and the infrastructure. A Bitcoin
backend must be available when the member synchronizes or broadcasts. The
operator must cooperate with joins and updates. Neither must supply a fresh
proof or signature for this client's unilateral exit after downtime.

This follows [spec section 9.7](https://github.com/lightninglabs/layertree/blob/f64cc0e4d4471e32f7fd8294ecb0b1c7547c1166/spec/layertree.md#97-data-retention-and-proof-maintenance).
Members retain their leaf and path. They can process missed tree spends from
the chain when they return. After the lease height, an eviction pays the
committed exit key. Ordinary leaves have no old-state challenge window.
Channel leaves have separate liveness requirements and are outside this MVP.

## Build and run

Run from the repository root. The Makefile prepares the pinned backend:

```sh
make build
```

The client expects an existing private regtest node with the LayerTree flag
and `txindex=1`. It reads the node's localhost HTTP RPC settings from its
configuration file. It never starts a node itself.

```sh
./bin/l3d init -state alice.json
./bin/l3d status -state alice.json
./bin/l3d attach -state alice.json -rpc-config btcd.conf -proof offer.json
./bin/l3d join -state alice.json -rpc-config btcd.conf -amount 100000 -evict-after 100000 -funding TXID:VOUT -out join.json
```

`offer.json` is a `lite.Proof`. It contains creation parameters, the current
outpoint, the allocated position, root and sibling path. Its leaf is null for
an empty slot. Attaching verifies the proof against a confirmed on-chain
output and verifies ownership when the slot is occupied. A cohort joiner can
attach the full path and own leaf after that join confirms.

The join proposal already contains the client's funding signature. The
operator adds its tree signature and returns the proposal. The demo implements
that handoff using the Go transaction signer. There is no operator RPC service
in this version.

```sh
./bin/l3d submit -state alice.json -rpc-config btcd.conf -request join-signed.json
./bin/l3d sync -state alice.json -rpc-config btcd.conf
./bin/l3d update -state alice.json -rpc-config btcd.conf -amount 150000 -evict-after 100000 -funding CHANGE_TXID:VOUT -out update.json
```

An update requires the same operator handoff. `amount` is the new balance.
The client signs the owner slot and funding input. The operator signs only
its slot. A decrease returns the released value to the client's change
output. The client pins the proposal transaction ID and checks actual chain
prevouts and every input script before broadcasting it.

An exit has no operator step:

```sh
./bin/l3d exit -state alice.json -rpc-config btcd.conf -funding FEE_TXID:VOUT -out exit.json
./bin/l3d submit -state alice.json -rpc-config btcd.conf -request exit.json
./bin/l3d sync -state alice.json -rpc-config btcd.conf
```

This client pays exact amounts on exit in every mode, using a separate fee input. Funding inputs
must be ordinary BIP86 outputs belonging to the address printed by `init`.
The default fee is 20,000 sats for the demo and can be set with `-fee`.
Confirmed exits and evictions leave a payout receipt in the state file.
The receipt's output uses the raw exit key, which is retained in the wallet.

## What it stores

The state file contains one private key, one leaf, its sibling path, tree
creation parameters, the current outpoint and a block checkpoint. An
outstanding proposal also records its transaction ID and intended new leaf.
The client stores no other participants' leaf records and no full tree.
Storage grows with depth rather than member count. Proposal files are
separate and contain the transaction and public signing data.

Files are written atomically with mode 0600. A command holds an OS file lock
while using its wallet. The private key is unencrypted in this MVP. Back up
the entire state file, including the leaf and path. A key alone is not a
replacement for join-time proof data, particularly for a grafted cohort.

## Catch-up

1. Check the saved block hash against the backend's active chain.
2. Scan blocks since that checkpoint and follow spends of the known tree
   outpoint.
3. Execute each tree input with the patched native script engine. Observe
   the tagged node and leaf preimages the covenant constructs.
4. Combine those nodes with the stored path. Recover the member's path
   under the new root and verify that root against output 0.
5. Retain only the member's updated proof and the new checkpoint.

Only hash-linked nodes reachable from the output commitment are used. Invalid
witnesses, mismatched roots, wrong owners and missing proof data are errors.
An amount-only operator top-up updates the member's leaf without requiring
the client to have been online. An eviction is recognized by the removal and
its exact payout. A competing confirmed tree spend invalidates an outstanding
proposal, allowing a subsequent command to rebuild against the new outpoint.

## Verified demonstration

```sh
make demo-lite
```

The runner starts a real regtest backend and invokes the client as a new
process for every command. Alice joins and updates, then her client stops.
Bob joins, updates and exits. The operator also tops up Alice by 10,000 sats.
The chain advances 148 blocks while Alice's saved file remains unchanged.
An unsigned cooperative renewal cannot block her from preparing an exit.
Her next exit command recovers her current proof and exits 160,000 sats
without an operator signature. A second member is evicted while offline and
finds its full 50,000 sat payout on its next invocation.

`lite-report.json` records the active wallet size and outcome. Raw transactions
and the operator's independent state snapshots are saved beside it. The
operator harness does not import the client's private key into its signer.

## Current limits

This is a lightweight member client using a trusted, indexed btcd RPC backend.
It is not an SPV header and inclusion-proof verifier. Catch-up currently
fetches full blocks since the checkpoint and historical prevouts, so bandwidth
and replay work grow with the time offline and tree activity.

It supports one membership per state file across the nine combinations in
[the mode guide](../mvp/MODES.md). Use separate state files for simultaneous
memberships. It does not support detached trees, reclaimable receive leaves,
tower exit mode, exit-consent variants or channel leaves.
The CLI exposes joins, updates and exits. It can follow other members'
claims and cohort transitions during catch-up. A reorganization of the saved
checkpoint fails closed and needs an earlier valid state backup. It does not
automatically roll back through reorgs, estimate fees, select coins, encrypt
keys or provide a network operator service.

## Choosing a mode

```sh
./bin/l3d modes
./bin/l3d attach -state alice.json -rpc-config btcd.conf -proof offer.json -tier ccv -profile dense
```

The optional selection flags reject an offer for a different mode. Every
attach verifies the offered parameters and proof against the confirmed
Bitcoin output. The tree's tier is fixed when created. `ccv` uses C2 numerics,
and `ccv0` uses C0 numerics. Existing dense MID state files remain readable.

For sparse trees, derive a candidate slot locally before requesting an offer:

```sh
./bin/l3d locate -state alice.json -tier mid -profile sparse -depth 31
```

Send the public owner key, position and nonce to the operator. It checks
vacancy and supplies the proof. `locate` and `status` work without a backend.
If the slot is occupied, rerun `locate` with `-start-nonce` set beyond the
previous nonce, then request a new operator offer.

MIN supports the reference's dense configuration only. The client attaches
to a leaf allocated in its funding transaction and can exit. It rejects
`join` and `update` because those scripts do not exist in the reference MIN
set. Its final-leaf terminal exit is also unsupported by the reference.

`join`, `update` and `exit` prepare individual member transactions. The
operator and transaction libraries can assemble cohort batches, and the
client follows them during catch-up. Collecting batch signatures from the
standalone member CLI is not implemented.
