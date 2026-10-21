//go:build layertree

package lite

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/GeorgeTsagk/L3D/operator"
	"github.com/GeorgeTsagk/L3D/protocol"
	"github.com/GeorgeTsagk/L3D/script"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/wire"
)

func modeFixture(t *testing.T, mode protocol.Capabilities) (fixture, uint64, uint64) {
	t.Helper()
	f := fixtureState(t)
	depth := 4
	if mode.Profile == "sparse" {
		depth = 32
		if mode.MaxDepth == 31 {
			depth = 31
		}
	}
	cfg, err := protocol.NewConfig(mode.Tier, mode.Profile, depth, xkey(f.keys[0]), [32]byte{42})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := cfg.Options()
	if err != nil {
		t.Fatal(err)
	}
	tr := tree.New(depth)
	positions := []uint64{2, 3}
	for i := 0; i < 2; i++ {
		leaf := testLeaf(f.keys[i+1], f.keys[0], uint64((i+2)*50000))
		if mode.Profile == "sparse" {
			for !leaf.PosLoMinimal(depth) && mode.MaxDepth == 31 {
				leaf.Nonce++
			}
			positions[i] = leaf.Position(depth)
		}
		tr.Leaves[positions[i]] = leaf
	}
	f.st, err = txn.NewTreeState(tr, opts, wire.OutPoint{})
	if err != nil {
		t.Fatal(err)
	}
	return f, positions[0], positions[1]
}
func TestAllModesMemberBuildersAndRecovery(t *testing.T) {
	for _, mode := range protocol.Modes() {
		t.Run(mode.Tier+"-"+mode.Profile, func(t *testing.T) {
			for _, operation := range []string{"JOIN_0", "UPDATE", "DECREASE", "EXIT_1"} {
				if mode.Tier == "min" && operation != "EXIT_1" {
					continue
				}
				f, alice, bob := modeFixture(t, mode)
				pos := alice
				var leaf *tree.Leaf
				var reference *txn.Spend
				var err error
				op := operation
				switch operation {
				case "JOIN_0":
					delete(f.st.Tree.Leaves, alice)
					leaf = testLeaf(f.keys[1], f.keys[0], 50000)
					if mode.Profile == "sparse" {
						for mode.MaxDepth == 31 && !leaf.PosLoMinimal(f.st.Tree.Depth) {
							leaf.Nonce++
						}
						pos = leaf.Position(f.st.Tree.Depth)
					}
					reference, _, err = txn.BuildJoin(f.st, 0, pos, map[uint64]*tree.Leaf{0: leaf}, &f.opts)
				case "UPDATE", "DECREASE":
					l := *f.st.Tree.Leaves[alice]
					l.Amount += 10000
					if operation == "DECREASE" {
						l.Amount -= 20000
					}
					l.Seq++
					leaf = &l
					op = "UPDATE"
					reference, _, err = txn.BuildUpdate(f.st, pos, leaf, &f.opts)
				case "EXIT_1":
					reference, _, err = txn.BuildExit(f.st, []txn.ExitStep{{Pos: pos}}, &f.opts)
				}
				if err != nil {
					t.Fatal(err)
				}
				p, err := ProofFor(f.st, pos)
				if err != nil {
					t.Fatal(err)
				}
				got, err := p.Build(op, leaf, f.opts)
				if err != nil {
					t.Fatal(err)
				}
				if err = reference.Sign(f.keys...); err != nil {
					t.Fatal(err)
				}
				if err = got.Sign(f.keys...); err != nil {
					t.Fatal(err)
				}
				var a, b bytes.Buffer
				reference.Tx.Serialize(&a)
				got.Tx.Serialize(&b)
				if !bytes.Equal(a.Bytes(), b.Bytes()) {
					t.Fatalf("%s differs from reference", operation)
				}
				native(t, got)
				next, payout, err := Advance(p, got.Tx, got.PrevOuts, leaf)
				if err != nil {
					t.Fatalf("%s recovery: %v", operation, err)
				}
				if op == "EXIT_1" {
					if payout == nil {
						t.Fatal("exit missing payout")
					}
				} else if next == nil || *next.Leaf != *leaf {
					t.Fatal("wrong recovered leaf")
				}
				// Existing members can recover while another member exits.
				f, alice, bob = modeFixture(t, mode)
				p, err = ProofFor(f.st, alice)
				if err != nil {
					t.Fatal(err)
				}
				spend, tr, err := txn.BuildExit(f.st, []txn.ExitStep{{Pos: bob}}, &f.opts)
				if err != nil {
					t.Fatal(err)
				}
				if err = spend.Sign(f.keys...); err != nil {
					t.Fatal(err)
				}
				next, payout, err = Advance(p, spend.Tx, spend.PrevOuts)
				if err != nil {
					t.Fatal(err)
				}
				if payout != nil || next.Root != tr.Root() || !reflect.DeepEqual(next.Path, tr.Path(alice, 0)) {
					t.Fatal("offline proof recovery differs")
				}
			}
		})
	}
}
func TestModeSelectionAndSparseBinding(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	pos, nonce, err := s.Locate("mid", "sparse", 31)
	if err != nil {
		t.Fatal(err)
	}
	key, _, _ := s.identity()
	l := &tree.Leaf{OwnerKey: key, Nonce: nonce}
	if l.Position(31) != pos || !l.PosLoMinimal(31) {
		t.Fatal("invalid derived sparse location")
	}
	for _, c := range []struct {
		tier, profile string
		depth         int
	}{{"min", "sparse", 31}, {"mid", "sparse", 24}, {"mid", "dense", 4}, {"invalid", "sparse", 31}} {
		if _, _, err = s.Locate(c.tier, c.profile, c.depth); err == nil {
			t.Fatal("invalid mode accepted")
		}
	}
	f, alice, _ := modeFixture(t, protocol.Capabilities{Tier: "full", Profile: "sparse", MaxDepth: 63})
	p, err := ProofFor(f.st, alice)
	if err != nil {
		t.Fatal(err)
	}
	p.Leaf.Nonce++
	p.Root, _ = tree.Fold(p.Leaf.Node(), p.Path)
	if err = p.Validate(); err == nil {
		t.Fatal("wrong sparse key position accepted")
	}
	p, err = ProofFor(fixtureState(t).st, 2)
	if err != nil {
		t.Fatal(err)
	}
	p.Options = script.MidReferenceOptions(key, [32]byte{})
	if err = p.Validate(); err == nil {
		t.Fatal("mixed legacy and new config accepted")
	}
}
func TestOperatorReconstructsMemberRequest(t *testing.T) {
	for _, mode := range protocol.Modes() {
		if mode.Tier == "min" {
			continue
		}
		t.Run(mode.Tier+mode.Profile, func(t *testing.T) {
			f, alice, _ := modeFixture(t, mode)
			o, err := operator.New(f.st, f.keys[0])
			if err != nil {
				t.Fatal(err)
			}
			p, err := ProofFor(f.st, alice)
			if err != nil {
				t.Fatal(err)
			}
			l := *p.Leaf
			l.Amount += 10000
			l.Seq++
			spend, err := p.Build("UPDATE", &l, f.opts)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := spend.Sighash()
			if err != nil {
				t.Fatal(err)
			}
			for _, slot := range spend.Sigs {
				if slot.Key == l.OwnerKey {
					spend.Stack[slot.Index], err = txn.SignSchnorr(f.keys[1], hash)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			spend.Tx.TxOut[0].Value--
			if _, err = o.CoSignMember(spend, alice, &l); err == nil {
				t.Fatal("operator approved incorrect remainder")
			}
			spend.Tx.TxOut[0].Value++
			// User-controlled slot metadata must not determine what gets signed.
			spend.Sigs = nil
			next, err := o.CoSignMember(spend, alice, &l)
			if err != nil {
				t.Fatal(err)
			}
			native(t, spend)
			spend.Tx.TxOut[0].Value--
			if err = o.ApplyConfirmed(spend.Tx, next); err == nil {
				t.Fatal("operator installed incorrect output")
			}
			spend.Tx.TxOut[0].Value++
			if err = o.ApplyConfirmed(spend.Tx, next); err != nil {
				t.Fatal(err)
			}
			if o.State.OutPoint.Hash != spend.Tx.TxHash() {
				t.Fatal("operator did not advance outpoint")
			}
			if next.Leaves[alice].Amount != l.Amount {
				t.Fatal("wrong operator update")
			}
			if _, err = operator.New(f.st, f.keys[1]); err == nil {
				t.Fatal("wrong operator key accepted")
			}
		})
	}
}

func TestModeAmountLimitsAndTerminalExit(t *testing.T) {
	for _, mode := range protocol.Modes() {
		t.Run(mode.Tier+mode.Profile, func(t *testing.T) {
			f, alice, bob := modeFixture(t, mode)
			l := *f.st.Tree.Leaves[alice]
			l.Amount = 3000000000
			f.st.Tree.Leaves[alice] = &l
			p, err := ProofFor(f.st, alice)
			if mode.MaxSum == tree.MidTierMaxSum {
				if err == nil {
					t.Fatal("C0 sum limit bypassed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			spend, err := p.Build("EXIT_1", nil, f.opts)
			if err != nil {
				t.Fatal(err)
			}
			if err = spend.Sign(f.keys...); err != nil {
				t.Fatal(err)
			}
			native(t, spend)
			delete(f.st.Tree.Leaves, bob)
			p, err = ProofFor(f.st, alice)
			if err != nil {
				t.Fatal(err)
			}
			spend, err = p.Build("EXIT_1", nil, f.opts)
			if err != nil {
				t.Fatal(err)
			}
			if err = spend.Sign(f.keys...); err != nil {
				t.Fatal(err)
			}
			native(t, spend)
			next, payout, err := Advance(p, spend.Tx, spend.PrevOuts)
			if err != nil || next != nil || payout == nil || payout.Output.Value != 3000000000 {
				t.Fatalf("terminal exit: %v", err)
			}
		})
	}
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	_, n, err := s.LocateFrom("mid", "sparse", 31, 123)
	if err != nil || n < 123 {
		t.Fatalf("nonce retry: %d %v", n, err)
	}
}
