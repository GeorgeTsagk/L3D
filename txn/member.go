package txn

import (
	"fmt"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/btcsuite/btcd/wire"
)

// BuildMember builds an ordinary member transition from one authenticated
// path. The caller need not keep any other member's leaf.
func BuildMember(depth int, pos uint64, old *tree.Leaf, path []tree.PathStep,
	in TreeInput, options Options, operation string, next *tree.Leaf,
	opts TxOpts) (*Spend, error) {
	r, err := optionRules(options)
	if err != nil {
		return nil, err
	}
	if in.Tier != r.tier || depth < 1 || depth > 63 || len(path) != depth || pos >= uint64(1)<<uint(depth) {
		return nil, fmt.Errorf("invalid member proof parameters")
	}
	if r.tower || r.reclaimKey != nil || r.exitConsent {
		return nil, fmt.Errorf("unsupported member transition variant")
	}
	oldNode, newNode := tree.EmptyLeaf, tree.EmptyLeaf
	for _, l := range []*tree.Leaf{old, next} {
		if l != nil {
			if err = l.Validate(); err != nil {
				return nil, err
			}
			if r.sparse && (l.Position(depth) != pos || (r.tier.elements() && !l.PosLoMinimal(depth))) {
				return nil, fmt.Errorf("sparse leaf does not match its slot")
			}
		}
	}
	if old != nil {
		oldNode = old.Node()
	}
	if next != nil {
		newNode = next.Node()
	}
	for h, s := range path {
		if s.Dir != ((pos>>uint(h))&1 == 1) || s.Sibling.Sum >= tree.MaxSum {
			return nil, fmt.Errorf("invalid member path")
		}
	}
	root, err := tree.Fold(oldNode, path)
	if err != nil {
		return nil, err
	}
	newRoot, err := tree.Fold(newNode, path)
	if err != nil {
		return nil, err
	}
	if root.Hash != in.RootHash || root.Sum != uint64(in.Amount) || in.Amount < 0 || root.Sum >= tree.MaxSum || newRoot.Sum >= tree.MaxSum {
		return nil, fmt.Errorf("member proof does not match tree input")
	}
	if r.tier.elements() && (root.Sum > tree.MidTierMaxSum || newRoot.Sum > tree.MidTierMaxSum) {
		return nil, fmt.Errorf("tree exceeds tier sum limit")
	}
	tt, err := NewOptionsTapTree(depth, options)
	if err != nil {
		return nil, err
	}
	if tt.Root() != in.MerkleRoot {
		return nil, fmt.Errorf("tree script parameters do not match input")
	}
	if _, err = tt.Find(operation); err != nil {
		return nil, err
	}
	body := r.inputPad(root.Sum)
	body = append(body, r.levels(path, 0, oldNode, newNode)...)
	var sigs []SigSlot
	var payouts []Payout
	var bottom [][]byte
	switch operation {
	case "JOIN_0":
		if old != nil || next == nil {
			return nil, fmt.Errorf("join requires an empty slot and a new leaf")
		}
		if r.sparse {
			body = append(body, r.pos(next)...)
			body = append(body, r.leaf(next, false)...)
		} else {
			body = append(body, newNode.Hash[:], r.sum(newNode.Sum))
		}
		body, sigs = operatorSlot(r, body)
	case "UPDATE":
		if old == nil || next == nil {
			return nil, fmt.Errorf("update requires old and new leaves")
		}
		body = append(body, r.pos(old)...)
		body = append(body, r.leaf(old, false)...)
		body = append(body, r.leaf(next, false)...)
		sigs = []SigSlot{{Index: len(body), Key: old.OwnerKey, Role: "owner"}, {Index: len(body) + 1, Key: r.operatorKey, Role: "operator"}}
		body = append(body, nil, nil)
		bottom = r.ccvModeFlag(in.Amount, nil, newRoot.Sum)
	case "EXIT_1":
		if old == nil || next != nil {
			return nil, fmt.Errorf("exit requires an occupied slot")
		}
		body = append(body, r.pos(old)...)
		body = append(body, r.leaf(old, true)...)
		sigs = []SigSlot{{Index: len(body), Key: old.OwnerKey, Role: "owner"}}
		body = append(body, nil, nil)
		payouts = []Payout{{ExitKey: old.ExitKey, Amount: int64(old.Amount)}}
	default:
		return nil, fmt.Errorf("unsupported member operation %q", operation)
	}
	terminal := newRoot == tree.EmptyNode(depth)
	if terminal {
		if r.tier == Min {
			return nil, fmt.Errorf("MIN has no terminal exit")
		}
		if opts.Terminal == nil {
			opts.Terminal = wire.NewTxOut(0, []byte{0x6a})
		}
	} else if opts.Terminal != nil {
		return nil, fmt.Errorf("nonterminal transition has terminal output")
	}
	return finishRoot(in, tt, r, newRoot, operation, payouts, bottom, body, sigs, &opts)
}
