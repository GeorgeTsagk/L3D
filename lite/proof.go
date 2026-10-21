//go:build layertree

// Package lite implements an on-demand LayerTree member. It keeps one
// leaf and its path, and recovers newer paths from confirmed transactions.
package lite

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/GeorgeTsagk/L3D/protocol"
	"github.com/GeorgeTsagk/L3D/script"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/wire"
)

type Proof struct {
	Deployment *protocol.Config      `json:"deployment,omitempty"`
	Nonce      uint32                `json:"nonce,omitempty"`
	Depth      int                   `json:"depth"`
	Position   uint64                `json:"position"`
	Leaf       *tree.Leaf            `json:"leaf"`
	Path       []tree.PathStep       `json:"path"`
	Root       tree.Node             `json:"root"`
	OutPoint   wire.OutPoint         `json:"outpoint"`
	Options    script.MidTreeOptions `json:"options"`
}

func (p *Proof) node() tree.Node {
	if p.Leaf == nil {
		return tree.EmptyLeaf
	}
	return p.Leaf.Node()
}

// Parameters retains compatibility with the original dense MID wallet files.
func (p *Proof) Parameters() (protocol.Config, error) {
	if p.Deployment != nil {
		if !reflect.DeepEqual(p.Options, script.MidTreeOptions{}) {
			return protocol.Config{}, fmt.Errorf("proof mixes legacy and deployment parameters")
		}
		return *p.Deployment, nil
	}
	return protocol.FromOptions(p.Options)
}
func (p *Proof) Validate() error {
	c, err := p.Parameters()
	if err != nil {
		return err
	}
	if err = c.Validate(p.Depth); err != nil {
		return err
	}
	caps, err := c.Capabilities()
	if err != nil {
		return err
	}
	if len(p.Path) != p.Depth || p.Position >= uint64(1)<<uint(p.Depth) {
		return fmt.Errorf("invalid proof dimensions")
	}
	if p.Leaf != nil {
		if err = p.Leaf.Validate(); err != nil {
			return err
		}
		if c.Profile == "sparse" {
			if p.Leaf.Position(p.Depth) != p.Position {
				return fmt.Errorf("sparse leaf does not derive its position")
			}
			t, _ := c.OpcodeTier()
			if t.Mode() == script.ModeC0 && !p.Leaf.PosLoMinimal(p.Depth) {
				return fmt.Errorf("sparse nonce is not usable for this tier")
			}
		}
	}
	for h, step := range p.Path {
		if step.Dir != ((p.Position>>uint(h))&1 == 1) || step.Sibling.Sum > caps.MaxSum {
			return fmt.Errorf("invalid path at level %d", h)
		}
	}
	root, err := tree.Fold(p.node(), p.Path)
	if err != nil {
		return err
	}
	if root != p.Root || root.Sum > caps.MaxSum {
		return fmt.Errorf("proof does not match root")
	}
	return nil
}

func (p *Proof) Input() (txn.TreeInput, error) {
	if err := p.Validate(); err != nil {
		return txn.TreeInput{}, err
	}
	c, _ := p.Parameters()
	o, err := c.Options()
	if err != nil {
		return txn.TreeInput{}, err
	}
	tier, _ := c.OpcodeTier()
	return txn.TreeInput{OutPoint: p.OutPoint, Amount: int64(p.Root.Sum), RootHash: p.Root.Hash, MerkleRoot: o.TapTreeRoot(p.Depth), Tier: tier}, nil
}

func (p *Proof) CheckOutput(out *wire.TxOut) error {
	in, err := p.Input()
	if err != nil {
		return err
	}
	expected, err := in.TxOut()
	if err != nil {
		return err
	}
	if out == nil || expected.Value != out.Value || !bytes.Equal(expected.PkScript, out.PkScript) {
		return fmt.Errorf("proof is not committed by the on-chain output")
	}
	return nil
}

// Build creates JOIN_0, UPDATE or owner EXIT_1 from one path. No other
// participant's leaf record or operator private key is needed.
func (p *Proof) Build(operation string, next *tree.Leaf, opts txn.TxOpts) (*txn.Spend, error) {
	in, err := p.Input()
	if err != nil {
		return nil, err
	}
	c, _ := p.Parameters()
	options, err := c.Options()
	if err != nil {
		return nil, err
	}
	return txn.BuildMember(p.Depth, p.Position, p.Leaf, p.Path, in, options, operation, next, opts)
}

// ProofFor extracts the public information a member needs from operator state.
func ProofFor(st *txn.TreeState, pos uint64) (*Proof, error) {
	c, err := protocol.FromOptions(st.Options)
	if err != nil {
		return nil, err
	}
	p := &Proof{Depth: st.Tree.Depth, Position: pos, Leaf: st.Tree.Leaves[pos], Path: st.Tree.Path(pos, 0), Root: st.Tree.Root(), OutPoint: st.OutPoint, Deployment: &c}
	if p.Leaf != nil {
		p.Nonce = p.Leaf.Nonce
	}
	return p, p.Validate()
}
