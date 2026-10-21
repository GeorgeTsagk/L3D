//go:build layertree

// Package mvp demonstrates the reference LayerTree protocol against an
// experimental btcd node. The deterministic wallets are for private regtest.
package mvp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"

	"github.com/GeorgeTsagk/L3D/script"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
)

const fee int64 = 20000

type Config struct {
	BTCD, Dir string
	Output    io.Writer
}
type Event struct {
	Operation string            `json:"operation"`
	TxID      string            `json:"txid"`
	VBytes    int64             `json:"vbytes"`
	Fee       int64             `json:"fee_sat"`
	TreeTotal uint64            `json:"tree_total_sat"`
	Balances  map[string]uint64 `json:"balances_sat"`
}
type Report struct {
	Events            []Event           `json:"events"`
	Rejections        map[string]string `json:"rejections"`
	FinalTreeOutpoint string            `json:"final_tree_outpoint"`
	Success           bool              `json:"success"`
}
type coin struct {
	op  wire.OutPoint
	out *wire.TxOut
}
type actor struct {
	name string
	key  *btcec.PrivateKey
	x    [32]byte
	spk  []byte
	coin coin
}
type demo struct {
	cfg    Config
	rpc    *rpcclient.Client
	st     *txn.TreeState
	actors map[string]*actor
	report Report
}

func newActor(name string) *actor {
	seed := sha256.Sum256([]byte("layertree-regtest-demo/" + name))
	key, _ := btcec.PrivKeyFromBytes(seed[:])
	a := &actor{name: name, key: key}
	copy(a.x[:], schnorr.SerializePubKey(key.PubKey()))
	// Wallet funds are ordinary BIP86 outputs. Leaf payouts use the raw exit key.
	a.spk, _ = txscript.PayToTaprootScript(txscript.ComputeTaprootKeyNoScript(key.PubKey()))
	return a
}
func (a *actor) leaf(amount uint64, operator *actor) *tree.Leaf {
	return &tree.Leaf{Amount: amount, OwnerKey: a.x, ExitKey: a.x, EvictorKey: operator.x, EvictAfter: 100000, Seq: 1}
}

// Run creates an isolated chain, runs the lifecycle and writes raw transactions
// and confirmed state snapshots. It returns an error on any failed assertion.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	cfg.Dir = dir
	binary, err := filepath.Abs(cfg.BTCD)
	if err != nil {
		return nil, err
	}
	cfg.BTCD = binary
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// Refuse reuse so failures never silently overwrite a previous transcript.
	marker, err := os.OpenFile(filepath.Join(dir, "run.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("use an empty run directory: %w", err)
	}
	marker.Close()
	d := &demo{cfg: cfg, actors: make(map[string]*actor), report: Report{Rejections: make(map[string]string)}}
	for _, name := range []string{"operator", "alice", "bob", "carol", "dave", "eve"} {
		d.actors[name] = newActor(name)
	}
	operator := d.actors["operator"]
	addr, err := btcutil.NewAddressTaproot(operator.spk[2:], &chaincfg.RegressionNetParams)
	if err != nil {
		return nil, err
	}
	client, stop, err := startNode(ctx, cfg.BTCD, dir, addr.EncodeAddress())
	if err != nil {
		return nil, err
	}
	defer stop()
	d.rpc = client
	fmt.Fprintln(cfg.Output, "Mining private regtest funding and activating Taproot...")
	if err = d.bootstrap(); err != nil {
		return nil, err
	}
	if err = d.lifecycle(); err != nil {
		return nil, err
	}
	d.report.Success = true
	d.report.FinalTreeOutpoint = d.st.OutPoint.String()
	if err = writeJSON(filepath.Join(dir, "report.json"), &d.report); err != nil {
		return nil, err
	}
	fmt.Fprintf(cfg.Output, "PASS: %d confirmed transactions, %d rejected attacks. Artifacts: %s\n", len(d.report.Events), len(d.report.Rejections), dir)
	return &d.report, nil
}

func (d *demo) bootstrap() error {
	hashes, err := d.rpc.Generate(450)
	if err != nil {
		return err
	}
	block, err := d.rpc.GetBlock(hashes[0])
	if err != nil {
		return err
	}
	cb := block.Transactions[0]
	op := d.actors["operator"]
	old := coin{wire.OutPoint{Hash: cb.TxHash(), Index: 0}, cb.TxOut[0]}
	tr := tree.New(4)
	tr.Leaves[0] = op.leaf(1000000, op)
	// A unique lineage identifier is chosen before funding, avoiding a txid
	// self-reference. The specification permits any unique 32-byte identifier.
	treeID := tree.TreeID([32]byte(old.op.Hash), old.op.Index)
	opts := script.MidReferenceOptions(op.x, treeID)
	opts.JoinHeights = []int{0, 1}
	opts.RewriteHeights = []int{1, 2}
	d.st, err = txn.NewTreeState(tr, opts, wire.OutPoint{})
	if err != nil {
		return err
	}
	input, err := d.st.Input()
	if err != nil {
		return err
	}
	treeOut, err := input.TxOut()
	if err != nil {
		return err
	}
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: old.op, Sequence: txn.TreeSequence})
	tx.AddTxOut(treeOut)
	names := []string{"operator", "alice", "bob", "carol", "dave", "eve"}
	for _, name := range names {
		tx.AddTxOut(wire.NewTxOut(1000000, d.actors[name].spk))
	}
	tx.AddTxOut(wire.NewTxOut(old.out.Value-7000000-fee, op.spk))
	if err = signWallet(tx, 0, op, map[wire.OutPoint]*wire.TxOut{old.op: old.out}); err != nil {
		return err
	}
	if err = d.confirm("CREATE", tx, map[wire.OutPoint]*wire.TxOut{old.op: old.out}, tr); err != nil {
		return err
	}
	for i, name := range names {
		d.actors[name].coin = coin{wire.OutPoint{Hash: tx.TxHash(), Index: uint32(i + 1)}, tx.TxOut[i+1]}
	}
	return nil
}

func signWallet(tx *wire.MsgTx, idx int, a *actor, prevs map[wire.OutPoint]*wire.TxOut) error {
	fetch := txscript.NewMultiPrevOutFetcher(prevs)
	hashes := txscript.NewTxSigHashes(tx, fetch)
	digest, err := txscript.CalcTaprootSignatureHash(hashes, txscript.SigHashDefault, tx, idx, fetch)
	if err != nil {
		return err
	}
	key := txscript.TweakTaprootPrivKey(*a.key, nil)
	sig, err := schnorr.Sign(key, digest)
	if err != nil {
		return err
	}
	tx.TxIn[idx].Witness = wire.TxWitness{sig.Serialize()}
	return nil
}

func (d *demo) opts(payer *actor, deposit int64) (*txn.TxOpts, error) {
	change := payer.coin.out.Value - deposit - fee
	if change < 330 {
		return nil, fmt.Errorf("%s lacks funding", payer.name)
	}
	return &txn.TxOpts{Inputs: []txn.ExtraInput{{OutPoint: payer.coin.op, PrevOut: payer.coin.out, Sequence: txn.TreeSequence}}, Outputs: []*wire.TxOut{wire.NewTxOut(change, payer.spk)}}, nil
}

func (d *demo) sign(s *txn.Spend, payer *actor, signers ...*actor) error {
	keys := make([]*btcec.PrivateKey, len(signers))
	for i, a := range signers {
		keys[i] = a.key
	}
	if err := s.Sign(keys...); err != nil {
		return err
	}
	return signWallet(s.Tx, 1, payer, s.PrevOuts)
}

func verify(tx *wire.MsgTx, prevs map[wire.OutPoint]*wire.TxOut) error {
	return txscript.ValidateLayerTreeTransaction(tx, txscript.NewMultiPrevOutFetcher(prevs), txscript.StandardVerifyFlags|txscript.ScriptVerifyLayerTree, nil, nil)
}

func (d *demo) confirm(name string, tx *wire.MsgTx, prevs map[wire.OutPoint]*wire.TxOut, next *tree.Tree) error {
	return d.confirmTransaction(name, tx, prevs, next, true)
}

func (d *demo) confirmTransaction(name string, tx *wire.MsgTx, prevs map[wire.OutPoint]*wire.TxOut, next *tree.Tree, broadcast bool) error {
	if err := verify(tx, prevs); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	hashValue := tx.TxHash()
	hash := &hashValue
	if broadcast {
		if _, err := d.rpc.SendRawTransaction(tx, false); err != nil {
			return fmt.Errorf("%s broadcast: %w", name, err)
		}
	}
	blocks, err := d.rpc.Generate(1)
	if err != nil {
		return err
	}
	block, err := d.rpc.GetBlock(blocks[0])
	if err != nil {
		return err
	}
	found := false
	for _, candidate := range block.Transactions {
		if candidate.TxHash() == *hash {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s was not mined", name)
	}
	for _, in := range tx.TxIn {
		out, err := d.rpc.GetTxOut(&in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index, false)
		if err != nil {
			return err
		}
		if out != nil {
			return fmt.Errorf("%s input remains unspent", name)
		}
	}
	for i, out := range tx.TxOut {
		got, err := d.rpc.GetTxOut(hash, uint32(i), false)
		if err != nil {
			return err
		}
		if got == nil {
			return fmt.Errorf("%s output %d missing", name, i)
		}
		value, err := btcutil.NewAmount(got.Value)
		if err != nil {
			return err
		}
		if int64(value) != out.Value || got.ScriptPubKey.Hex != hex.EncodeToString(out.PkScript) {
			return fmt.Errorf("%s output %d differs", name, i)
		}
	}
	if next != nil {
		d.st.Tree = next
		d.st.OutPoint = wire.OutPoint{Hash: *hash, Index: 0}
		rootInput, err := d.st.Input()
		if err != nil {
			return err
		}
		expected, err := rootInput.TxOut()
		if err != nil {
			return err
		}
		if expected.Value != tx.TxOut[0].Value || !bytes.Equal(expected.PkScript, tx.TxOut[0].PkScript) {
			return fmt.Errorf("%s remainder mismatch", name)
		}
	} else {
		next = d.st.Tree
	}
	var inSum, outSum int64
	for _, in := range tx.TxIn {
		inSum += prevs[in.PreviousOutPoint].Value
	}
	for _, out := range tx.TxOut {
		outSum += out.Value
	}
	balances := make(map[string]uint64)
	for _, leaf := range next.Leaves {
		for name, a := range d.actors {
			if leaf.OwnerKey == a.x {
				balances[name] += leaf.Amount
			}
		}
	}
	ev := Event{Operation: name, TxID: hash.String(), VBytes: mempoolVSize(tx), Fee: inSum - outSum, TreeTotal: next.Root().Sum, Balances: balances}
	d.report.Events = append(d.report.Events, ev)
	var raw bytes.Buffer
	if err = tx.Serialize(&raw); err != nil {
		return err
	}
	prefix := filepath.Join(d.cfg.Dir, fmt.Sprintf("%02d-%s", len(d.report.Events), name))
	if err = os.WriteFile(prefix+".hex", []byte(hex.EncodeToString(raw.Bytes())+"\n"), 0600); err != nil {
		return err
	}
	proofs := make(map[uint64][]tree.PathStep)
	for pos := range next.Leaves {
		proofs[pos] = next.Path(pos, 0)
	}
	previous := make(map[string]*wire.TxOut)
	for op, out := range prevs {
		previous[op.String()] = out
	}
	if err = writeJSON(prefix+".json", struct {
		Event           Event
		Depth           int
		TreeOutpoint    string
		Root            tree.Node
		Leaves          map[uint64]*tree.Leaf
		Proofs          map[uint64][]tree.PathStep
		Options         txn.Options
		PreviousOutputs map[string]*wire.TxOut
	}{ev, next.Depth, d.st.OutPoint.String(), next.Root(), next.Leaves, proofs, d.st.Options, previous}); err != nil {
		return err
	}
	fmt.Fprintf(d.cfg.Output, "%-20s %5d vB  tree=%d sat  %s\n", name, ev.VBytes, ev.TreeTotal, hash)
	return nil
}

func mempoolVSize(tx *wire.MsgTx) int64 {
	return (blockchain.GetTransactionWeight(btcutil.NewTx(tx)) + 3) / 4
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

func (d *demo) accept(name string, s *txn.Spend, next *tree.Tree, payer *actor, signers ...*actor) error {
	if err := d.sign(s, payer, signers...); err != nil {
		return err
	}
	if err := d.confirm(name, s.Tx, s.PrevOuts, next); err != nil {
		return err
	}
	i := len(s.Tx.TxOut) - 1
	payer.coin = coin{wire.OutPoint{Hash: s.Tx.TxHash(), Index: uint32(i)}, s.Tx.TxOut[i]}
	return nil
}

func (d *demo) reject(name string, s *txn.Spend) error {
	if err := verify(s.Tx, s.PrevOuts); err == nil {
		return fmt.Errorf("%s accepted locally", name)
	}
	_, err := d.rpc.SendRawTransaction(s.Tx, false)
	if err == nil {
		return fmt.Errorf("%s accepted by node", name)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "script") {
		return fmt.Errorf("%s failed for an unexpected reason: %w", name, err)
	}
	d.report.Rejections[name] = err.Error()
	fmt.Fprintf(d.cfg.Output, "REJECT %s\n", name)
	return nil
}

func (d *demo) lifecycle() error {
	op, a, b, c, e := d.actors["operator"], d.actors["alice"], d.actors["bob"], d.actors["carol"], d.actors["eve"]
	for _, join := range []struct {
		a      *actor
		pos    uint64
		amount int64
	}{{a, 2, 100000}, {b, 3, 150000}} {
		opts, err := d.opts(join.a, join.amount)
		if err != nil {
			return err
		}
		s, next, err := txn.BuildJoin(d.st, 0, join.pos, map[uint64]*tree.Leaf{0: join.a.leaf(uint64(join.amount), op)}, opts)
		if err != nil {
			return err
		}
		if join.a == a {
			if err = d.sign(s, a, op); err != nil {
				return err
			}
			// Fully resign the changed transaction so covenant checks, rather than
			// an invalid funding signature, are responsible for rejecting it.
			original := append([]byte(nil), s.Tx.TxOut[0].PkScript...)
			s.Tx.TxOut[0].PkScript[2] ^= 1
			if err = d.sign(s, a, op); err != nil {
				return err
			}
			if err = d.reject("wrong-remainder-root", s); err != nil {
				return err
			}
			if err = d.rejectBlock(s); err != nil {
				return err
			}
			s.Tx.TxOut[0].PkScript = original
			if err = d.sign(s, a, op); err != nil {
				return err
			}
			slot := s.Sigs[0].Index
			s.Tx.TxIn[0].Witness[slot] = nil
			if err = d.reject("missing-operator-signature", s); err != nil {
				return err
			}
		}
		if err = d.accept("JOIN-"+join.a.name, s, next, join.a, op); err != nil {
			return err
		}
	}
	// A funded cohort uses one outer path for Carol and Dave.
	opts, err := d.opts(c, 200000)
	if err != nil {
		return err
	}
	s, next, err := txn.BuildJoin(d.st, 1, 2, map[uint64]*tree.Leaf{0: c.leaf(100000, op), 1: d.actors["dave"].leaf(100000, op)}, opts)
	if err != nil {
		return err
	}
	if err = d.accept("JOIN-cohort", s, next, c, op); err != nil {
		return err
	}
	// Alice adds 50k to her balance with owner and operator consent.
	updated := *d.st.Tree.Leaves[2]
	updated.Amount += 50000
	updated.Seq++
	opts, err = d.opts(a, 50000)
	if err != nil {
		return err
	}
	s, next, err = txn.BuildUpdate(d.st, 2, &updated, opts)
	if err != nil {
		return err
	}
	if err = d.sign(s, a, a, op); err != nil {
		return err
	}
	s.Tx.TxOut[0].Value--
	if err = d.sign(s, a, a, op); err != nil {
		return err
	}
	if err = d.reject("underfunded-remainder", s); err != nil {
		return err
	}
	s.Tx.TxOut[0].Value++
	if err = d.sign(s, a, a, op); err != nil {
		return err
	}
	s.Tx.TxIn[0].Witness[s.Sigs[0].Index] = nil
	if err = d.reject("missing-owner-signature", s); err != nil {
		return err
	}
	// Corrupt a sibling hash while keeping a valid transaction signature.
	if err = d.sign(s, a, a, op); err != nil {
		return err
	}
	changed := false
	for i, el := range s.Stack {
		if len(el) == 32 && i < len(s.Stack)-2 {
			bad := append([]byte(nil), el...)
			bad[0] ^= 1
			s.Tx.TxIn[0].Witness[i] = bad
			changed = true
			break
		}
	}
	if !changed {
		return fmt.Errorf("no path hash to mutate")
	}
	if err = d.reject("invalid-merkle-path", s); err != nil {
		return err
	}
	if err = d.accept("UPDATE-alice", s, next, a, a, op); err != nil {
		return err
	}
	// Bob transfers 10k to Alice in one cohort rewrite. No external deposit.
	after := map[uint64]*tree.Leaf{}
	for i := uint64(0); i < 2; i++ {
		l := *d.st.Tree.Leaves[2+i]
		after[i] = &l
	}
	after[0].Amount += 10000
	after[1].Amount -= 10000
	opts, err = d.opts(op, 0)
	if err != nil {
		return err
	}
	s, next, err = txn.BuildRewrite(d.st, 1, 1, after, false, opts)
	if err != nil {
		return err
	}
	if err = d.accept("REWRITE-payment", s, next, op, op, a, b); err != nil {
		return err
	}
	// A voucher transfers 25k from Bob to Eve with no operator signature.
	claim := e.leaf(25000, op)
	voucher, err := txn.SignVoucher(b.key, d.st.Options.(script.MidTreeOptions).TreeID, d.st.Tree.Leaves[3], claim)
	if err != nil {
		return err
	}
	opts, err = d.opts(e, 0)
	if err != nil {
		return err
	}
	s, next, err = txn.BuildClaim(d.st, 3, 6, claim, voucher, opts)
	if err != nil {
		return err
	}
	if err = d.accept("CLAIM-eve", s, next, e); err != nil {
		return err
	}
	// Exit signing receives only the owner's key. The operator is absent.
	var payouts []ownedCoin
	for _, exit := range []struct {
		a   *actor
		pos uint64
	}{{a, 2}, {b, 3}, {e, 6}} {
		opts, err = d.opts(exit.a, 0)
		if err != nil {
			return err
		}
		s, next, err = txn.BuildExit(d.st, []txn.ExitStep{{Pos: exit.pos}}, opts)
		if err != nil {
			return err
		}
		if len(s.Sigs) != 1 || s.Sigs[0].Key != exit.a.x {
			return fmt.Errorf("exit unexpectedly requires another signer")
		}
		if exit.a == a {
			if err = d.sign(s, a, a); err != nil {
				return err
			}
			s.Tx.TxOut[1].PkScript = op.spk
			if err = d.sign(s, a, a); err != nil {
				return err
			}
			if err = d.reject("redirected-exit-payout", s); err != nil {
				return err
			}
			s.Tx.TxOut[1].PkScript = tree.P2TR(a.x)
		}
		if err = d.accept("EXIT-"+exit.a.name, s, next, exit.a, exit.a); err != nil {
			return err
		}
		payouts = append(payouts, ownedCoin{exit.a, coin{wire.OutPoint{Hash: s.Tx.TxHash(), Index: 1}, s.Tx.TxOut[1]}})
	}
	// Two owners exit in a single transaction using EXIT_2.
	opts, err = d.opts(op, 0)
	if err != nil {
		return err
	}
	s, next, err = txn.BuildExit(d.st, []txn.ExitStep{{Pos: 4}, {Pos: 5}}, opts)
	if err != nil {
		return err
	}
	if err = d.accept("EXIT-batch", s, next, op, c, d.actors["dave"]); err != nil {
		return err
	}
	for i, owner := range []*actor{c, d.actors["dave"]} {
		payouts = append(payouts, ownedCoin{owner, coin{wire.OutPoint{Hash: s.Tx.TxHash(), Index: uint32(i + 1)}, s.Tx.TxOut[i+1]}})
	}
	if err = d.redeem(payouts); err != nil {
		return err
	}
	if len(d.st.Tree.Leaves) != 1 || d.st.Tree.Leaves[0].Amount != 1000000 {
		return fmt.Errorf("unaffected operator balance changed")
	}
	return nil
}
