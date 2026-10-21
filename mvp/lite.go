//go:build layertree

package mvp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/GeorgeTsagk/L3D/lite"
	"github.com/GeorgeTsagk/L3D/script"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
)

type LiteConfig struct {
	BTCD, Client, Dir string
	Output            io.Writer
}
type LiteReport struct {
	Success           bool   `json:"success"`
	ClientInvocations int    `json:"client_invocations"`
	OfflineBlocks     uint32 `json:"offline_blocks"`
	ActiveStateBytes  int    `json:"active_state_bytes"`
	ExitTxID          string `json:"exit_txid"`
	ExitAmount        uint64 `json:"exit_amount_sat"`
	EvictionObserved  bool   `json:"eviction_observed"`
	OperatorBalance   uint64 `json:"operator_balance_sat"`
}

// RunLite drives a real CLI as a fresh process for every action. The harness
// never imports Alice's private key into its signer or supplies her a full tree.
func RunLite(ctx context.Context, cfg LiteConfig) (*LiteReport, error) {
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}
	var err error
	cfg.Dir, err = filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	cfg.BTCD, err = filepath.Abs(cfg.BTCD)
	if err != nil {
		return nil, err
	}
	cfg.Client, err = filepath.Abs(cfg.Client)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(cfg.Dir, 0700); err != nil {
		return nil, err
	}
	marker, err := os.OpenFile(filepath.Join(cfg.Dir, "run.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	marker.Close()
	statePath := filepath.Join(cfg.Dir, "alice.json")
	rpcPath := filepath.Join(cfg.Dir, "btcd.conf")
	report := &LiteReport{}
	cli := func(command string, args ...string) (lite.Status, error) {
		all := append([]string{command, "-state", statePath, "-rpc-config", rpcPath}, args...)
		cmd := exec.CommandContext(ctx, cfg.Client, all...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return lite.Status{}, fmt.Errorf("client %s: %v: %s", command, err, stderr.String())
		}
		var status lite.Status
		if err = json.Unmarshal(out, &status); err != nil {
			return status, err
		}
		report.ClientInvocations++
		fmt.Fprintf(cfg.Output, "client %-7s exited: state=%s balance=%d height=%d\n", command, status.State, status.Balance, status.Height)
		return status, nil
	}
	status, err := cli("init")
	if err != nil {
		return nil, err
	}
	d := &demo{cfg: Config{BTCD: cfg.BTCD, Dir: cfg.Dir, Output: cfg.Output}, actors: make(map[string]*actor), report: Report{Rejections: make(map[string]string)}}
	for _, name := range []string{"operator", "bob", "carol", "dave", "eve"} {
		d.actors[name] = newActor(name)
	}
	alice := &actor{name: "alice"}
	pub, err := hex.DecodeString(status.Owner)
	if err != nil {
		return nil, err
	}
	copy(alice.x[:], pub)
	alice.spk, err = hex.DecodeString(status.FundingScript)
	if err != nil {
		return nil, err
	}
	d.actors["alice"] = alice
	op, bob := d.actors["operator"], d.actors["bob"]
	addr, err := btcutil.NewAddressTaproot(op.spk[2:], &chaincfg.RegressionNetParams)
	if err != nil {
		return nil, err
	}
	rpc, stop, err := startNode(ctx, cfg.BTCD, cfg.Dir, addr.EncodeAddress())
	if err != nil {
		return nil, err
	}
	defer stop()
	d.rpc = rpc
	if err = d.bootstrap(); err != nil {
		return nil, err
	}
	// Send only an empty-slot proof and creation parameters to the client.
	offer := lite.Proof{Depth: 4, Position: 2, Path: d.st.Tree.Path(2, 0), Root: d.st.Tree.Root(), OutPoint: d.st.OutPoint, Options: d.st.Options.(script.MidTreeOptions)}
	offerPath := filepath.Join(cfg.Dir, "offer.json")
	if err = lite.WriteJSON(offerPath, offer); err != nil {
		return nil, err
	}
	if _, err = cli("attach", "-proof", offerPath); err != nil {
		return nil, err
	}
	request := filepath.Join(cfg.Dir, "proposal.json")
	cooperate := func(command string, amount uint64, lease uint32) error {
		_, err := cli(command, "-amount", fmt.Sprint(amount), "-evict-after", fmt.Sprint(lease), "-funding", alice.coin.op.String(), "-out", request)
		if err != nil {
			return err
		}
		s, err := lite.ReadSpend(request)
		if err != nil {
			return err
		}
		// The operator signs only its slot. Alice's key never enters this process.
		digest, err := s.Sighash()
		if err != nil {
			return err
		}
		for _, slot := range s.Sigs {
			if slot.Role == "operator" && slot.Key == op.x {
				sig, err := txn.SignSchnorr(op.key, digest)
				if err != nil {
					return err
				}
				s.Stack[slot.Index] = sig
			}
		}
		if err = lite.AttachWitness(s); err != nil {
			return err
		}
		if err = lite.WriteSpend(request, s); err != nil {
			return err
		}
		if _, err = cli("submit", "-request", request); err != nil {
			return err
		}
		next := d.st.Tree.Clone()
		leaf := alice.leaf(amount, op)
		leaf.EvictAfter = lease
		if command == "update" {
			leaf.Seq = next.Leaves[2].Seq + 1
		}
		next.Leaves[2] = leaf
		if err = d.confirmTransaction("LITE-"+command, s.Tx, s.PrevOuts, next, false); err != nil {
			return err
		}
		i := len(s.Tx.TxOut) - 1
		alice.coin = coin{wire.OutPoint{Hash: s.Tx.TxHash(), Index: uint32(i)}, s.Tx.TxOut[i]}
		status, err = cli("sync")
		if err != nil {
			return err
		}
		if status.Balance != amount {
			return fmt.Errorf("client balance differs after %s", command)
		}
		return nil
	}
	if err = cooperate("join", 100000, 100000); err != nil {
		return nil, err
	}
	if err = cooperate("update", 150000, 100000); err != nil {
		return nil, err
	}
	// The operator withholds a renewal signature. Alice can still prepare
	// an exit. Neither proposal is broadcast. Later tree activity invalidates
	// these old proposals, and the returning client rebuilds automatically.
	if _, err = cli("update", "-amount", "150000", "-evict-after", "100001", "-funding", alice.coin.op.String(), "-out", request); err != nil {
		return nil, err
	}
	if _, err = cli("exit", "-funding", alice.coin.op.String(), "-out", filepath.Join(cfg.Dir, "unused-exit.json")); err != nil {
		return nil, err
	}
	asleep, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(cfg.Output, "Alice is offline. Other users now change the tree.")
	opts, err := d.opts(bob, 100000)
	if err != nil {
		return nil, err
	}
	s, next, err := txn.BuildJoin(d.st, 0, 3, map[uint64]*tree.Leaf{0: bob.leaf(100000, op)}, opts)
	if err != nil {
		return nil, err
	}
	if err = d.accept("OFFLINE-join-bob", s, next, bob, op); err != nil {
		return nil, err
	}
	changed := *d.st.Tree.Leaves[3]
	changed.Amount += 25000
	changed.Seq++
	opts, err = d.opts(bob, 25000)
	if err != nil {
		return nil, err
	}
	s, next, err = txn.BuildUpdate(d.st, 3, &changed, opts)
	if err != nil {
		return nil, err
	}
	if err = d.accept("OFFLINE-update-bob", s, next, bob, bob, op); err != nil {
		return nil, err
	}
	// An operator-funded amount-only top-up changes Alice's own leaf while
	// she sleeps. Her retained body and the published hash preimages recover it.
	a, b := *d.st.Tree.Leaves[2], *d.st.Tree.Leaves[3]
	a.Amount += 10000
	opts, err = d.opts(op, 10000)
	if err != nil {
		return nil, err
	}
	s, next, err = txn.BuildRewrite(d.st, 1, 1, map[uint64]*tree.Leaf{0: &a, 1: &b}, false, opts)
	if err != nil {
		return nil, err
	}
	if err = d.accept("OFFLINE-topup-alice", s, next, op, op); err != nil {
		return nil, err
	}
	opts, err = d.opts(bob, 0)
	if err != nil {
		return nil, err
	}
	s, next, err = txn.BuildExit(d.st, []txn.ExitStep{{Pos: 3}}, opts)
	if err != nil {
		return nil, err
	}
	if err = d.accept("OFFLINE-exit-bob", s, next, bob, bob); err != nil {
		return nil, err
	}
	if _, err = rpc.Generate(144); err != nil {
		return nil, err
	}
	report.OfflineBlocks = 148
	still, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(asleep, still) {
		return nil, fmt.Errorf("client state changed while client was absent")
	}
	// The operator code stops participating. The client's exit automatically
	// catches up from block witnesses and uses only Alice's signature.
	if _, err = cli("exit", "-funding", alice.coin.op.String(), "-out", request); err != nil {
		return nil, err
	}
	s, err = lite.ReadSpend(request)
	if err != nil {
		return nil, err
	}
	if len(s.Sigs) != 1 || s.Sigs[0].Key != alice.x {
		return nil, fmt.Errorf("exit needs a non-owner signature")
	}
	if _, err = cli("submit", "-request", request); err != nil {
		return nil, err
	}
	next = d.st.Tree.Clone()
	delete(next.Leaves, 2)
	if err = d.confirmTransaction("LITE-exit-alice", s.Tx, s.PrevOuts, next, false); err != nil {
		return nil, err
	}
	status, err = cli("sync")
	if err != nil {
		return nil, err
	}
	if status.State != "exited" || status.Payout == nil || status.Payout.Output.Value != 160000 {
		return nil, fmt.Errorf("client did not recover its correct exit")
	}
	report.ExitTxID = s.Tx.TxHash().String()
	report.ExitAmount = 160000
	report.ActiveStateBytes = len(asleep)
	report.OperatorBalance = d.st.Tree.Leaves[0].Amount
	// Exercise offline expiry separately with a second fresh CLI wallet.
	statePath = filepath.Join(cfg.Dir, "sleeping.json")
	status, err = cli("init")
	if err != nil {
		return nil, err
	}
	sleeping := &actor{name: "sleeping"}
	pub, err = hex.DecodeString(status.Owner)
	if err != nil {
		return nil, err
	}
	copy(sleeping.x[:], pub)
	height, err := rpc.GetBlockCount()
	if err != nil {
		return nil, err
	}
	sleepLeaf := sleeping.leaf(50000, op)
	sleepLeaf.EvictAfter = uint32(height + 2)
	opts, err = d.opts(op, 50000)
	if err != nil {
		return nil, err
	}
	s, next, err = txn.BuildJoin(d.st, 0, 4, map[uint64]*tree.Leaf{0: sleepLeaf}, opts)
	if err != nil {
		return nil, err
	}
	if err = d.accept("SLEEPING-join", s, next, op, op); err != nil {
		return nil, err
	}
	offer = lite.Proof{Depth: 4, Position: 4, Leaf: sleepLeaf, Path: d.st.Tree.Path(4, 0), Root: d.st.Tree.Root(), OutPoint: d.st.OutPoint, Options: d.st.Options.(script.MidTreeOptions)}
	if err = lite.WriteJSON(offerPath, offer); err != nil {
		return nil, err
	}
	if _, err = cli("attach", "-proof", offerPath); err != nil {
		return nil, err
	}
	if _, err = rpc.Generate(3); err != nil {
		return nil, err
	}
	opts, err = d.opts(op, 0)
	if err != nil {
		return nil, err
	}
	opts.LockTime = sleepLeaf.EvictAfter
	s, next, err = txn.BuildExit(d.st, []txn.ExitStep{{Pos: 4, Evict: true}}, opts)
	if err != nil {
		return nil, err
	}
	if err = d.accept("SLEEPING-eviction", s, next, op, op); err != nil {
		return nil, err
	}
	status, err = cli("sync")
	if err != nil {
		return nil, err
	}
	if status.State != "exited" || status.Payout == nil || status.Payout.Output.Value != 50000 {
		return nil, fmt.Errorf("offline eviction payout not recovered")
	}
	report.EvictionObserved = true
	report.Success = true
	if err = writeJSON(filepath.Join(cfg.Dir, "lite-report.json"), report); err != nil {
		return nil, err
	}
	fmt.Fprintf(cfg.Output, "PASS: %d short-lived client invocations. Offline for %d blocks. Exit: %d sats. State file: %d bytes.\n", report.ClientInvocations, report.OfflineBlocks, report.ExitAmount, report.ActiveStateBytes)
	return report, nil
}
