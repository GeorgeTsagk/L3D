//go:build layertree

package lite

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type children struct{ left, right tree.Node }
type observations struct {
	nodes  map[[32]byte]children
	leaves map[[32]byte]*tree.Leaf
	roots  map[[32]byte]bool
}

func tagPrefix(tag string) []byte {
	h := sha256.Sum256([]byte(tag))
	return append(append([]byte{}, h[:]...), h[:]...)
}

var nodePrefix = tagPrefix(tree.TagNode)
var leafPrefix = tagPrefix(tree.TagLeaf)
var statePrefix = tagPrefix(tree.TagState)

func newObservations(p *Proof) *observations {
	o := &observations{nodes: make(map[[32]byte]children), leaves: make(map[[32]byte]*tree.Leaf), roots: make(map[[32]byte]bool)}
	if p.Leaf != nil {
		o.leaves[p.Leaf.Hash()] = p.Leaf
	}
	n := p.node()
	for _, step := range p.Path {
		left, right := n, step.Sibling
		if step.Dir {
			left, right = right, left
		}
		n = tree.Parent(left, right)
		o.nodes[n.Hash] = children{left, right}
	}
	return o
}

// observe captures tagged Merkle preimages already assembled by the native
// covenant interpreter. It does not require a separate witness format parser.
// Every observation is addressed by its hash. Only the output-committed root
// is used to select a path after successful script execution.
func (o *observations) observe(info *txscript.StepInfo) error {
	if len(info.Stack) == 0 {
		return nil
	}
	b := info.Stack[len(info.Stack)-1]
	switch {
	case len(b) == 146 && bytes.HasPrefix(b, nodePrefix):
		decode := func(raw []byte) (tree.Node, bool) {
			var n tree.Node
			copy(n.Hash[:], raw[:32])
			n.Sum = binary.LittleEndian.Uint64(raw[32:40])
			return n, raw[40] == 1 && n.Sum <= tree.MaxSum-1
		}
		left, ok := decode(b[64:105])
		if !ok {
			return nil
		}
		right, ok := decode(b[105:146])
		if !ok || left.Sum+right.Sum > tree.MaxSum-1 {
			return nil
		}
		hash := sha256.Sum256(b)
		o.nodes[hash] = children{left, right}
		o.roots[hash] = true
	case len(b) == 64+tree.LeafLen && bytes.HasPrefix(b, leafPrefix):
		leaf, err := tree.ParseLeaf(b[64:])
		if err == nil && leaf.Validate() == nil {
			o.leaves[sha256.Sum256(b)] = leaf
		}
	case len(b) == 96 && bytes.HasPrefix(b, statePrefix):
		var root [32]byte
		copy(root[:], b[64:])
		o.roots[root] = true
	}
	return nil
}

func (o *observations) path(root tree.Node, depth int, pos uint64) (*tree.Leaf, []tree.PathStep, error) {
	n := root
	path := make([]tree.PathStep, depth)
	for h := depth - 1; h >= 0; h-- {
		pair, ok := o.nodes[n.Hash]
		if !ok {
			return nil, nil, fmt.Errorf("missing Merkle preimage at height %d", h+1)
		}
		if tree.Parent(pair.left, pair.right) != n {
			return nil, nil, fmt.Errorf("inconsistent Merkle sum")
		}
		right := (pos>>uint(h))&1 == 1
		if right {
			path[h] = tree.PathStep{Dir: true, Sibling: pair.left}
			n = pair.right
		} else {
			path[h] = tree.PathStep{Sibling: pair.right}
			n = pair.left
		}
	}
	if n == tree.EmptyLeaf {
		return nil, path, nil
	}
	leaf, ok := o.leaves[n.Hash]
	if !ok || leaf.Node() != n {
		return nil, nil, fmt.Errorf("missing member leaf preimage")
	}
	return leaf, path, nil
}

// Advance updates a member proof using only a confirmed spend and its prevouts.
// The caller establishes chain membership. It never supplies other leaves.
// A nil next proof means this member was removed and paid to its exit key.
func Advance(p *Proof, tx *wire.MsgTx, prevs map[wire.OutPoint]*wire.TxOut, known ...*tree.Leaf) (*Proof, *Coin, error) {
	if len(tx.TxIn) == 0 || len(tx.TxOut) == 0 || tx.TxIn[0].PreviousOutPoint != p.OutPoint {
		return nil, nil, fmt.Errorf("transaction does not continue the known tree")
	}
	if err := p.CheckOutput(prevs[p.OutPoint]); err != nil {
		return nil, nil, err
	}
	for _, in := range tx.TxIn {
		if prevs[in.PreviousOutPoint] == nil {
			return nil, nil, fmt.Errorf("missing prevout")
		}
	}
	config, err := p.Parameters()
	if err != nil {
		return nil, nil, err
	}
	options, err := config.Options()
	if err != nil {
		return nil, nil, err
	}
	tier, _ := config.OpcodeTier()
	tt, err := txn.NewOptionsTapTree(p.Depth, options)
	if err != nil {
		return nil, nil, err
	}
	witness := tx.TxIn[0].Witness
	if len(witness) < 2 {
		return nil, nil, fmt.Errorf("missing script-path witness")
	}
	// This version follows the scripts committed by the accepted profile.
	// Unknown versions, key-path spends and annexes fail closed.
	found := false
	for _, l := range tt.Leaves {
		if l.Version == tier.LeafVersion() && bytes.Equal(l.Script, witness[len(witness)-2]) {
			found = true
			break
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("unsupported transition script")
	}
	obs := newObservations(p)
	for _, leaf := range known {
		if leaf != nil {
			obs.leaves[leaf.Hash()] = leaf
		}
	}
	fetch := txscript.NewMultiPrevOutFetcher(prevs)
	prev := prevs[p.OutPoint]
	vm, err := txscript.NewDebugEngine(prev.PkScript, tx, 0, txscript.StandardVerifyFlags|txscript.ScriptVerifyLayerTree, nil, txscript.NewTxSigHashes(tx, fetch), prev.Value, fetch, obs.observe)
	if err == nil {
		err = vm.Execute()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("invalid tree transition: %w", err)
	}
	var next *Proof
	for hash := range obs.roots {
		spk, err := tier.ScriptPubKey(hash, tt.Root())
		if err != nil {
			continue
		}
		if !bytes.Equal(spk, tx.TxOut[0].PkScript) {
			continue
		}
		candidate := *p
		candidate.Root = tree.Node{Hash: hash, Sum: uint64(tx.TxOut[0].Value)}
		candidate.OutPoint = wire.OutPoint{Hash: tx.TxHash(), Index: 0}
		candidate.Leaf, candidate.Path, err = obs.path(candidate.Root, p.Depth, p.Position)
		if err != nil {
			return nil, nil, err
		}
		if err = candidate.Validate(); err != nil {
			return nil, nil, err
		}
		next = &candidate
		break
	}
	if next != nil && next.Leaf != nil {
		return next, nil, nil
	}
	if p.Leaf == nil {
		if next == nil {
			return nil, nil, fmt.Errorf("empty slot lost its tree")
		}
		return next, nil, nil
	}
	// Removal, including offline eviction, must have an exact payout.
	for i, out := range tx.TxOut {
		if out.Value == int64(p.Leaf.Amount) && bytes.Equal(out.PkScript, tree.P2TR(p.Leaf.ExitKey)) {
			return nil, &Coin{OutPoint: wire.OutPoint{Hash: tx.TxHash(), Index: uint32(i)}, Output: *out}, nil
		}
	}
	return nil, nil, fmt.Errorf("member disappeared without the expected payout")
}
