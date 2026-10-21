// Package operator coordinates admission and cooperative transitions. It
// holds the full tree and its own key, never member private keys.
package operator

import (
	"bytes"
	"fmt"

	"github.com/GeorgeTsagk/L3D/protocol"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/wire"
)

type Descriptor struct {
	Name     string          `json:"name"`
	Depth    int             `json:"depth"`
	Config   protocol.Config `json:"config"`
	OutPoint wire.OutPoint   `json:"outpoint"`
	Root     tree.Node       `json:"root"`
}

type Coordinator struct {
	State  *txn.TreeState
	key    *btcec.PrivateKey
	config protocol.Config
}

func New(st *txn.TreeState, key *btcec.PrivateKey) (*Coordinator, error) {
	if st == nil || st.Tree == nil || key == nil {
		return nil, fmt.Errorf("missing operator state or key")
	}
	c, err := protocol.FromOptions(st.Options)
	if err != nil {
		return nil, err
	}
	if err = c.Validate(st.Tree.Depth); err != nil {
		return nil, err
	}
	if st.TapTree.Root() != st.Options.TapTreeRoot(st.Tree.Depth) {
		return nil, fmt.Errorf("operator script parameters disagree")
	}
	if c.Tier != "min" && !bytes.Equal(c.Rules.OperatorKey[:], schnorr.SerializePubKey(key.PubKey())) {
		return nil, fmt.Errorf("wrong operator key")
	}
	caps, _ := c.Capabilities()
	var sum uint64
	for pos, l := range st.Tree.Leaves {
		if l == nil {
			return nil, fmt.Errorf("nil leaf")
		}
		var err error
		sum, err = tree.AddSum(sum, l.Amount)
		if err != nil || sum > caps.MaxSum {
			return nil, fmt.Errorf("tree sum out of range")
		}
		if pos >= uint64(1)<<uint(st.Tree.Depth) {
			return nil, fmt.Errorf("leaf outside tree")
		}
		if err = l.Validate(); err != nil {
			return nil, err
		}
		if c.Profile == "sparse" && l.Position(st.Tree.Depth) != pos {
			return nil, fmt.Errorf("invalid sparse position")
		}
	}
	if st.Tree.Root().Sum > caps.MaxSum {
		return nil, fmt.Errorf("tree exceeds tier sum cap")
	}
	return &Coordinator{State: st, key: key, config: c}, nil
}
func (o *Coordinator) Describe(name string) Descriptor {
	return Descriptor{Name: name, Depth: o.State.Tree.Depth, Config: o.config, OutPoint: o.State.OutPoint, Root: o.State.Tree.Root()}
}

// CoSignMember reconstructs the requested change from operator state. It
// matches the transaction and witness before adding only the operator's
// signature. nextLeaf is public intent supplied by the requesting member.
// The caller supplies chain-verified prevouts and persists the returned tree
// only after confirmation. The proposal remains anchored to the old outpoint.
func (o *Coordinator) CoSignMember(s *txn.Spend, pos uint64, nextLeaf *tree.Leaf) (*tree.Tree, error) {
	if nextLeaf == nil {
		return nil, fmt.Errorf("missing requested leaf")
	}
	if s == nil || s.Tx == nil || len(s.Tx.TxIn) < 1 || len(s.Tx.TxOut) < 1 {
		return nil, fmt.Errorf("incomplete member request")
	}
	if s.Tx.TxIn[0].PreviousOutPoint != o.State.OutPoint {
		return nil, fmt.Errorf("stale tree outpoint")
	}
	var opts txn.TxOpts
	opts.LockTime = s.Tx.LockTime
	for _, in := range s.Tx.TxIn[1:] {
		prev := s.PrevOuts[in.PreviousOutPoint]
		if prev == nil {
			return nil, fmt.Errorf("missing funding prevout")
		}
		opts.Inputs = append(opts.Inputs, txn.ExtraInput{OutPoint: in.PreviousOutPoint, PrevOut: prev, Sequence: in.Sequence})
	}
	opts.Outputs = s.Tx.TxOut[1:]
	var expected *txn.Spend
	var next *tree.Tree
	var err error
	switch s.Leaf.Name {
	case "JOIN_0":
		expected, next, err = txn.BuildJoin(o.State, 0, pos, map[uint64]*tree.Leaf{0: nextLeaf}, &opts)
	case "UPDATE":
		expected, next, err = txn.BuildUpdate(o.State, pos, nextLeaf, &opts)
	default:
		return nil, fmt.Errorf("operator member request must be JOIN_0 or UPDATE")
	}
	if err != nil {
		return nil, err
	}
	if expected.Tx.TxHash() != s.Tx.TxHash() || expected.Leaf.Version != s.Leaf.Version || !bytes.Equal(expected.Leaf.Script, s.Leaf.Script) || !bytes.Equal(expected.ControlBlock, s.ControlBlock) || len(expected.Stack) != len(s.Stack) {
		return nil, fmt.Errorf("proposal does not match requested tree change")
	}
	slots := make(map[int]txn.SigSlot)
	for _, slot := range expected.Sigs {
		slots[slot.Index] = slot
	}
	digest, err := expected.Sighash()
	if err != nil {
		return nil, err
	}
	for i, v := range expected.Stack {
		slot, signature := slots[i]
		if !signature {
			if !bytes.Equal(v, s.Stack[i]) {
				return nil, fmt.Errorf("proposal witness differs at %d", i)
			}
			continue
		}
		if slot.Role == "operator" {
			continue
		}
		if err = txn.VerifySchnorr(slot.Key, digest, s.Stack[i]); err != nil {
			return nil, fmt.Errorf("member authorization: %w", err)
		}
	}
	// Ignore untrusted signature slot labels. Only reconstructed slots count.
	s.Sigs = expected.Sigs
	for _, slot := range expected.Sigs {
		if slot.Role != "operator" {
			continue
		}
		sig, err := txn.SignSchnorr(o.key, digest)
		if err != nil {
			return nil, err
		}
		s.Stack[slot.Index] = sig
	}
	if err = txn.TapscriptSpend(s.Tx, 0, s.PrevOuts, s.Leaf.Script, s.Leaf.Version, s.ControlBlock, s.Stack); err != nil {
		return nil, err
	}
	return next, nil
}

// ApplyConfirmed installs a transition after the caller has established its
// confirmation and validated its inputs. The output must commit to next.
func (o *Coordinator) ApplyConfirmed(tx *wire.MsgTx, next *tree.Tree) error {
	if tx == nil || next == nil || len(tx.TxIn) == 0 || len(tx.TxOut) == 0 || tx.TxIn[0].PreviousOutPoint != o.State.OutPoint || next.Depth != o.State.Tree.Depth {
		return fmt.Errorf("invalid confirmed transition")
	}
	input, err := o.State.Input()
	if err != nil {
		return err
	}
	tier, _ := o.config.OpcodeTier()
	spk, err := tier.ScriptPubKey(next.Root().Hash, input.MerkleRoot)
	if err != nil {
		return err
	}
	if tx.TxOut[0].Value != int64(next.Root().Sum) || !bytes.Equal(spk, tx.TxOut[0].PkScript) {
		return fmt.Errorf("confirmed tree output differs")
	}
	o.State.Tree = next
	o.State.OutPoint = wire.OutPoint{Hash: tx.TxHash(), Index: 0}
	return nil
}
