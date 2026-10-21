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
	"github.com/GeorgeTsagk/L3D/operator"
	"github.com/GeorgeTsagk/L3D/protocol"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
)

type ModeResult struct {
	Mode                                 string `json:"mode"`
	Profile                              string `json:"profile"`
	Depth                                int    `json:"depth"`
	JoinedAtCreation                     bool   `json:"joined_at_creation"`
	JoinVB, UpdateVB, DecreaseVB, ExitVB int64
	BatchJoins, BatchUpdates, BatchExits int
	OfflineBlocks                        int64  `json:"offline_blocks"`
	ClientInvocations                    int    `json:"client_invocations"`
	ExitAmount                           uint64 `json:"exit_amount_sat"`
	StateBytes                           int    `json:"active_state_bytes"`
	Success                              bool   `json:"success"`
}
type ModesReport struct {
	CCVAccounting bool         `json:"ccv_transaction_accounting"`
	Success       bool         `json:"success"`
	Modes         []ModeResult `json:"modes"`
}

type modeClient struct {
	ctx                context.Context
	binary, state, rpc string
	calls              int
}

func (c *modeClient) call(command string, args ...string) ([]byte, error) {
	all := append([]string{command, "-state", c.state, "-rpc-config", c.rpc}, args...)
	cmd := exec.CommandContext(c.ctx, c.binary, all...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v: %s", command, err, stderr.String())
	}
	c.calls++
	return out, nil
}
func (c *modeClient) status(command string, args ...string) (lite.Status, error) {
	out, err := c.call(command, args...)
	if err != nil {
		return lite.Status{}, err
	}
	var s lite.Status
	err = json.Unmarshal(out, &s)
	return s, err
}

// RunModes hosts all supported configurations simultaneously on one private
// btcd node. Each member command is a separate process. Operator code knows
// only the member's public keys and proposed leaf.
func RunModes(ctx context.Context, cfg LiteConfig) (*ModesReport, error) {
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
	op := newActor("operator")
	addr, err := btcutil.NewAddressTaproot(op.spk[2:], &chaincfg.RegressionNetParams)
	if err != nil {
		return nil, err
	}
	rpc, stop, err := startNode(ctx, cfg.BTCD, cfg.Dir, addr.EncodeAddress())
	if err != nil {
		return nil, err
	}
	defer stop()
	blocks, err := rpc.Generate(450)
	if err != nil {
		return nil, err
	}
	report := &ModesReport{}
	var catalog []operator.Descriptor
	for i, mode := range protocol.Modes() {
		name := mode.Tier + "-" + mode.Profile
		dir := filepath.Join(cfg.Dir, name)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		client := &modeClient{ctx: ctx, binary: cfg.Client, state: filepath.Join(dir, "member.json"), rpc: filepath.Join(cfg.Dir, "btcd.conf")}
		status, err := client.status("init")
		if err != nil {
			return nil, err
		}
		d := &demo{cfg: Config{Dir: dir, Output: cfg.Output}, rpc: rpc, actors: map[string]*actor{}, report: Report{Rejections: map[string]string{}}}
		for _, n := range []string{"operator", "bob", "carol", "dave", "eve"} {
			d.actors[n] = newActor(n)
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
		depth := 8
		if mode.Profile == "sparse" {
			depth = 32
			if mode.MaxDepth == 31 {
				depth = 31
			}
		}
		conf, err := protocol.NewConfig(mode.Tier, mode.Profile, depth, op.x, tree.TreeID([32]byte(*blocks[i]), uint32(i)))
		if err != nil {
			return nil, err
		}
		options, err := conf.Options()
		if err != nil {
			return nil, err
		}
		tr := tree.New(depth)
		seed := op.leaf(1000000, op)
		seedPos := modePosition(mode, depth, seed, tr, 0)
		tr.Leaves[seedPos] = seed
		pos, nonce := uint64(2), uint32(0)
		if mode.Profile == "sparse" {
			raw, err := client.call("locate", "-tier", mode.Tier, "-profile", mode.Profile, "-depth", fmt.Sprint(depth))
			if err != nil {
				return nil, err
			}
			var location struct {
				Position uint64
				Nonce    uint32
			}
			if err = json.Unmarshal(raw, &location); err != nil {
				return nil, err
			}
			pos, nonce = location.Position, location.Nonce
			if tr.Leaves[pos] != nil {
				return nil, fmt.Errorf("unexpected sparse collision")
			}
		}
		result := ModeResult{Mode: mode.Tier, Profile: mode.Profile, Depth: depth}
		bobPos := uint64(3)
		if mode.Tier == "min" {
			tr.Leaves[pos] = alice.leaf(100000, op)
			tr.Leaves[bobPos] = bob.leaf(50000, op)
			result.JoinedAtCreation = true
		}
		d.st, err = txn.NewTreeState(tr, options, wire.OutPoint{})
		if err != nil {
			return nil, err
		}
		block, err := rpc.GetBlock(blocks[i])
		if err != nil {
			return nil, err
		}
		if err = d.fundMode(block.Transactions[0]); err != nil {
			return nil, fmt.Errorf("%s funding: %w", name, err)
		}
		coord, err := operator.New(d.st, op.key)
		if err != nil {
			return nil, err
		}
		catalog = append(catalog, coord.Describe(name))
		if err = lite.WriteJSON(filepath.Join(cfg.Dir, "catalog.json"), catalog); err != nil {
			return nil, err
		}
		offer, err := lite.ProofFor(d.st, pos)
		if err != nil {
			return nil, err
		}
		offer.Nonce = nonce
		offerPath := filepath.Join(dir, "offer.json")
		if err = lite.WriteJSON(offerPath, offer); err != nil {
			return nil, err
		}
		// A client choosing another mode must reject this offer before attaching.
		if _, err = client.status("attach", "-proof", offerPath, "-tier", "invalid"); err == nil {
			return nil, fmt.Errorf("mode selection was not enforced")
		}
		if _, err = client.status("attach", "-proof", offerPath, "-tier", mode.Tier, "-profile", mode.Profile); err != nil {
			return nil, err
		}
		request := filepath.Join(dir, "request.json")
		cooperate := func(command string, amount uint64) (int64, error) {
			if _, err := client.status(command, "-amount", fmt.Sprint(amount), "-evict-after", "100000", "-funding", alice.coin.op.String(), "-out", request); err != nil {
				return 0, err
			}
			s, err := lite.ReadSpend(request)
			if err != nil {
				return 0, err
			}
			// Prevouts passed to the operator come from its own node.
			for _, in := range s.Tx.TxIn {
				prev, err := lite.UTXO(rpc, in.PreviousOutPoint, true)
				if err != nil {
					return 0, err
				}
				s.PrevOuts[in.PreviousOutPoint] = prev
			}
			nextLeaf := alice.leaf(amount, op)
			nextLeaf.Nonce = nonce
			if command == "update" {
				nextLeaf.Seq = d.st.Tree.Leaves[pos].Seq + 1
			}
			next, err := coord.CoSignMember(s, pos, nextLeaf)
			if err != nil {
				return 0, err
			}
			if err = lite.WriteSpend(request, s); err != nil {
				return 0, err
			}
			if _, err = client.status("submit", "-request", request); err != nil {
				return 0, err
			}
			if err = d.confirmTransaction(name+"-"+command, s.Tx, s.PrevOuts, next, false); err != nil {
				return 0, err
			}
			j := len(s.Tx.TxOut) - 1
			alice.coin = coin{wire.OutPoint{Hash: s.Tx.TxHash(), Index: uint32(j)}, s.Tx.TxOut[j]}
			status, err := client.status("sync")
			if err != nil {
				return 0, err
			}
			if status.Balance != amount {
				return 0, fmt.Errorf("wrong balance after %s", command)
			}
			return mempoolVSize(s.Tx), nil
		}
		expected := uint64(100000)
		if mode.Tier != "min" {
			result.JoinVB, err = cooperate("join", 100000)
			if err != nil {
				return nil, fmt.Errorf("%s join: %w", name, err)
			}
			result.UpdateVB, err = cooperate("update", 150000)
			if err != nil {
				return nil, fmt.Errorf("%s update: %w", name, err)
			}
			result.DecreaseVB, err = cooperate("update", 140000)
			if err != nil {
				return nil, fmt.Errorf("%s decrease: %w", name, err)
			}
			expected = 140000
		} else {
			if _, err = client.status("join", "-amount", "100000", "-evict-after", "100000", "-funding", alice.coin.op.String(), "-out", request); err == nil {
				return nil, fmt.Errorf("MIN offered a nonexistent join")
			}
			if _, err = client.status("update", "-amount", "100000", "-evict-after", "100000", "-funding", alice.coin.op.String(), "-out", request); err == nil {
				return nil, fmt.Errorf("MIN offered a nonexistent update")
			}
		}
		asleep, err := os.ReadFile(client.state)
		if err != nil {
			return nil, err
		}
		startHeight, err := rpc.GetBlockCount()
		if err != nil {
			return nil, err
		}
		if mode.Tier != "min" {
			leaf := bob.leaf(50000, op)
			bobPos = modePosition(mode, depth, leaf, d.st.Tree, 3)
			opts, err := d.opts(bob, 50000)
			if err != nil {
				return nil, err
			}
			s, next, err := txn.BuildJoin(d.st, 0, bobPos, map[uint64]*tree.Leaf{0: leaf}, opts)
			if err != nil {
				return nil, err
			}
			// Re-sign an incorrect remainder so the covenant rejects it.
			s.Tx.TxOut[0].Value--
			if err = d.sign(s, bob, op); err != nil {
				return nil, err
			}
			if err = d.reject(name+"-bad-remainder", s); err != nil {
				return nil, err
			}
			s.Tx.TxOut[0].Value++
			if err = d.accept(name+"-bob-join", s, next, bob, op); err != nil {
				return nil, err
			}
			updated := *d.st.Tree.Leaves[bobPos]
			updated.Amount += 10000
			updated.Seq++
			opts, err = d.opts(bob, 10000)
			if err != nil {
				return nil, err
			}
			s, next, err = txn.BuildUpdate(d.st, bobPos, &updated, opts)
			if err != nil {
				return nil, err
			}
			if err = d.accept(name+"-bob-update", s, next, bob, bob, op); err != nil {
				return nil, err
			}
		}
		opts, err := d.opts(bob, 0)
		if err != nil {
			return nil, err
		}
		s, next, err := txn.BuildExit(d.st, []txn.ExitStep{{Pos: bobPos}}, opts)
		if err != nil {
			return nil, err
		}
		if err = d.accept(name+"-bob-exit", s, next, bob, bob); err != nil {
			return nil, err
		}
		if mode.Cohorts {
			members := []*actor{bob, d.actors["carol"], d.actors["dave"], d.actors["eve"]}
			leaves := map[uint64]*tree.Leaf{}
			for j, a := range members {
				leaves[uint64(j)] = a.leaf(10000, op)
			}
			opts, err = d.opts(op, 40000)
			if err != nil {
				return nil, err
			}
			s, next, err = txn.BuildJoin(d.st, 2, 1, leaves, opts)
			if err != nil {
				return nil, err
			}
			if err = d.accept(name+"-cohort-join", s, next, op, op); err != nil {
				return nil, err
			}
			result.BatchJoins = 4
			for j := range leaves {
				l := *leaves[j]
				l.Amount += 1000
				l.Seq++
				leaves[j] = &l
			}
			opts, err = d.opts(op, 4000)
			if err != nil {
				return nil, err
			}
			s, next, err = txn.BuildRewrite(d.st, 2, 1, leaves, false, opts)
			if err != nil {
				return nil, err
			}
			signers := append([]*actor{op}, members...)
			if err = d.accept(name+"-cohort-update", s, next, op, signers...); err != nil {
				return nil, err
			}
			result.BatchUpdates = 4
			opts, err = d.opts(op, 0)
			if err != nil {
				return nil, err
			}
			s, next, err = txn.BuildRewrite(d.st, 2, 1, map[uint64]*tree.Leaf{}, false, opts)
			if err != nil {
				return nil, err
			}
			if err = d.accept(name+"-cohort-exit", s, next, op, signers...); err != nil {
				return nil, err
			}
			result.BatchExits = 4
		}
		if _, err = rpc.Generate(10); err != nil {
			return nil, err
		}
		height, err := rpc.GetBlockCount()
		if err != nil {
			return nil, err
		}
		result.OfflineBlocks = height - startHeight
		still, err := os.ReadFile(client.state)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(still, asleep) {
			return nil, fmt.Errorf("member state changed while offline")
		}
		if _, err = client.status("exit", "-funding", alice.coin.op.String(), "-out", request); err != nil {
			return nil, fmt.Errorf("%s exit preparation: %w", name, err)
		}
		s, err = lite.ReadSpend(request)
		if err != nil {
			return nil, err
		}
		if len(s.Sigs) != 1 || s.Sigs[0].Key != alice.x {
			return nil, fmt.Errorf("exit requested another signer")
		}
		if _, err = client.status("submit", "-request", request); err != nil {
			return nil, err
		}
		next = d.st.Tree.Clone()
		delete(next.Leaves, pos)
		if err = d.confirmTransaction(name+"-alice-exit", s.Tx, s.PrevOuts, next, false); err != nil {
			return nil, err
		}
		status, err = client.status("sync")
		if err != nil {
			return nil, err
		}
		if status.State != "exited" || status.Payout == nil || status.Payout.Output.Value != int64(expected) {
			return nil, fmt.Errorf("incorrect exit payout")
		}
		result.ExitVB = mempoolVSize(s.Tx)
		result.ExitAmount = expected
		result.ClientInvocations = client.calls
		result.StateBytes = len(asleep)
		result.Success = true
		report.Modes = append(report.Modes, result)
		if err = writeJSON(filepath.Join(dir, "report.json"), d.report); err != nil {
			return nil, err
		}
		fmt.Fprintf(cfg.Output, "PASS %s: %d commands, %d blocks offline, %d sat unilateral exit\n", name, result.ClientInvocations, result.OfflineBlocks, result.ExitAmount)
	}
	accountingBlock, err := rpc.GetBlock(blocks[30])
	if err != nil {
		return nil, err
	}
	if err = checkCCVAccounting(rpc, op, accountingBlock.Transactions[0]); err != nil {
		return nil, err
	}
	report.CCVAccounting = true
	report.Success = true
	if err = writeJSON(filepath.Join(cfg.Dir, "modes-report.json"), report); err != nil {
		return nil, err
	}
	return report, nil
}

func modePosition(mode protocol.Capabilities, depth int, l *tree.Leaf, tr *tree.Tree, dense uint64) uint64 {
	if mode.Profile == "dense" {
		return dense
	}
	for {
		pos := l.Position(depth)
		if tr.Leaves[pos] == nil && (mode.MaxDepth != 31 || l.PosLoMinimal(depth)) {
			return pos
		}
		l.Nonce++
	}
}
func (d *demo) fundMode(cb *wire.MsgTx) error {
	old := coin{wire.OutPoint{Hash: cb.TxHash(), Index: 0}, cb.TxOut[0]}
	input, err := d.st.Input()
	if err != nil {
		return err
	}
	out, err := input.TxOut()
	if err != nil {
		return err
	}
	op := d.actors["operator"]
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: old.op, Sequence: txn.TreeSequence})
	tx.AddTxOut(out)
	names := []string{"operator", "alice", "bob", "carol", "dave", "eve"}
	for _, name := range names {
		tx.AddTxOut(wire.NewTxOut(1000000, d.actors[name].spk))
	}
	tx.AddTxOut(wire.NewTxOut(old.out.Value-out.Value-6000000-fee, op.spk))
	prevs := map[wire.OutPoint]*wire.TxOut{old.op: old.out}
	if err = signWallet(tx, 0, op, prevs); err != nil {
		return err
	}
	if err = d.confirm("CREATE", tx, prevs, d.st.Tree); err != nil {
		return err
	}
	for i, name := range names {
		d.actors[name].coin = coin{wire.OutPoint{Hash: tx.TxHash(), Index: uint32(i + 1)}, tx.TxOut[i+1]}
	}
	return nil
}
