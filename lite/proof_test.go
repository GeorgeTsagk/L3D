//go:build layertree

package lite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GeorgeTsagk/L3D/script"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type fixture struct {
	st   *txn.TreeState
	keys []*btcec.PrivateKey
	opts txn.TxOpts
}

func fixtureState(t *testing.T) fixture {
	t.Helper()
	f := fixture{}
	for _, name := range []string{"operator", "alice", "bob"} {
		seed := sha256.Sum256([]byte(name))
		k, _ := btcec.PrivKeyFromBytes(seed[:])
		f.keys = append(f.keys, k)
	}
	opts := script.MidReferenceOptions(xkey(f.keys[0]), sha256.Sum256([]byte("lite test")))
	opts.JoinHeights = []int{0, 1}
	opts.RewriteHeights = []int{1, 2}
	tr := tree.New(4)
	tr.Leaves[2] = testLeaf(f.keys[1], f.keys[0], 100000)
	tr.Leaves[3] = testLeaf(f.keys[2], f.keys[0], 150000)
	var err error
	f.st, err = txn.NewTreeState(tr, opts, wire.OutPoint{})
	if err != nil {
		t.Fatal(err)
	}
	f.opts = txn.TxOpts{Inputs: []txn.ExtraInput{{OutPoint: wire.OutPoint{Index: 1}, PrevOut: wire.NewTxOut(1000000, []byte{0x51}), Sequence: txn.TreeSequence}}, Outputs: []*wire.TxOut{wire.NewTxOut(500000, []byte{0x51})}}
	return f
}
func xkey(k *btcec.PrivateKey) [32]byte {
	var x [32]byte
	copy(x[:], schnorr.SerializePubKey(k.PubKey()))
	return x
}
func testLeaf(k, op *btcec.PrivateKey, amount uint64) *tree.Leaf {
	return &tree.Leaf{Amount: amount, OwnerKey: xkey(k), ExitKey: xkey(k), EvictorKey: xkey(op), EvictAfter: 10000, Seq: 1}
}
func proofAt(st *txn.TreeState, pos uint64) *Proof {
	return &Proof{Depth: st.Tree.Depth, Position: pos, Leaf: st.Tree.Leaves[pos], Path: st.Tree.Path(pos, 0), Root: st.Tree.Root(), OutPoint: st.OutPoint, Options: st.Options.(script.MidTreeOptions)}
}
func native(t *testing.T, s *txn.Spend) {
	t.Helper()
	fetch := txscript.NewMultiPrevOutFetcher(s.PrevOuts)
	prev := s.PrevOuts[s.Tx.TxIn[0].PreviousOutPoint]
	vm, err := txscript.NewEngine(prev.PkScript, s.Tx, 0, txscript.StandardVerifyFlags|txscript.ScriptVerifyLayerTree, nil, txscript.NewTxSigHashes(s.Tx, fetch), prev.Value, fetch)
	if err == nil {
		err = vm.Execute()
	}
	if err != nil {
		t.Fatal(err)
	}
}
func TestProofBuildersMatchReference(t *testing.T) {
	for _, operation := range []string{"JOIN_0", "UPDATE", "EXIT_1", "TERMINAL"} {
		t.Run(operation, func(t *testing.T) {
			f := fixtureState(t)
			pos := uint64(2)
			var newLeaf *tree.Leaf
			var reference *txn.Spend
			var err error
			switch operation {
			case "JOIN_0":
				pos = 4
				newLeaf = testLeaf(f.keys[1], f.keys[0], 50000)
				reference, _, err = txn.BuildJoin(f.st, 0, pos, map[uint64]*tree.Leaf{0: newLeaf}, &f.opts)
			case "UPDATE":
				newLeaf = testLeaf(f.keys[1], f.keys[0], 120000)
				newLeaf.Seq++
				reference, _, err = txn.BuildUpdate(f.st, pos, newLeaf, &f.opts)
			case "EXIT_1":
				reference, _, err = txn.BuildExit(f.st, []txn.ExitStep{{Pos: pos}}, &f.opts)
			case "TERMINAL":
				delete(f.st.Tree.Leaves, 3)
				f.opts.Terminal = wire.NewTxOut(0, []byte{0x6a})
				reference, _, err = txn.BuildExit(f.st, []txn.ExitStep{{Pos: pos}}, &f.opts)
				operation = "EXIT_1"
			}
			if err != nil {
				t.Fatal(err)
			}
			p := proofAt(f.st, pos)
			got, err := p.Build(operation, newLeaf, f.opts)
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
				t.Fatal("lite builder differs from reference witness transaction")
			}
			native(t, got)
		})
	}
}

func TestReplayMissedTransitions(t *testing.T) {
	f := fixtureState(t)
	p := proofAt(f.st, 2)
	apply := func(s *txn.Spend, next *tree.Tree) {
		t.Helper()
		if err := s.Sign(f.keys...); err != nil {
			t.Fatal(err)
		}
		got, payout, err := Advance(p, s.Tx, s.PrevOuts)
		if err != nil {
			t.Fatal(err)
		}
		if payout != nil || got == nil {
			t.Fatal("unexpected removal")
		}
		if got.Root != next.Root() || !reflect.DeepEqual(got.Path, next.Path(2, 0)) || *got.Leaf != *next.Leaves[2] {
			t.Fatal("recovered proof differs from full tree")
		}
		p = got
		f.st.Tree = next
		f.st.OutPoint = got.OutPoint
	}
	join := testLeaf(f.keys[2], f.keys[0], 50000)
	s, next, err := txn.BuildJoin(f.st, 1, 2, map[uint64]*tree.Leaf{0: join, 1: join}, &f.opts)
	if err != nil {
		t.Fatal(err)
	}
	apply(s, next)
	changed := *f.st.Tree.Leaves[3]
	changed.Amount += 1000
	changed.Seq++
	s, next, err = txn.BuildUpdate(f.st, 3, &changed, &f.opts)
	if err != nil {
		t.Fatal(err)
	}
	apply(s, next)
	a, b := *f.st.Tree.Leaves[2], *f.st.Tree.Leaves[3]
	a.Amount += 10000
	s, next, err = txn.BuildRewrite(f.st, 1, 1, map[uint64]*tree.Leaf{0: &a, 1: &b}, false, &f.opts)
	if err != nil {
		t.Fatal(err)
	}
	apply(s, next)
	claim := testLeaf(f.keys[2], f.keys[0], 1000)
	voucher, err := txn.SignVoucher(f.keys[2], p.Options.TreeID, f.st.Tree.Leaves[3], claim)
	if err != nil {
		t.Fatal(err)
	}
	s, next, err = txn.BuildClaim(f.st, 3, 6, claim, voucher, &f.opts)
	if err != nil {
		t.Fatal(err)
	}
	apply(s, next)
	s, next, err = txn.BuildExit(f.st, []txn.ExitStep{{Pos: 4}, {Pos: 5}}, &f.opts)
	if err != nil {
		t.Fatal(err)
	}
	apply(s, next)
	// Corrupt a witness while retaining a transaction with valid signatures.
	s, _, err = txn.BuildExit(f.st, []txn.ExitStep{{Pos: 3}}, &f.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Sign(f.keys...); err != nil {
		t.Fatal(err)
	}
	for i, b := range s.Tx.TxIn[0].Witness {
		if len(b) == 32 && i < len(s.Stack)-2 {
			s.Tx.TxIn[0].Witness[i] = bytes.Repeat([]byte{9}, 32)
			break
		}
	}
	if _, _, err = Advance(p, s.Tx, s.PrevOuts); err == nil {
		t.Fatal("corrupt witness accepted")
	}
	s, err = p.Build("EXIT_1", nil, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Sign(f.keys[1]); err != nil {
		t.Fatal(err)
	}
	got, payout, err := Advance(p, s.Tx, s.PrevOuts)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil || payout == nil || payout.Output.Value != 110000 {
		t.Fatal("wrong payout")
	}
}

func TestRejectInvalidProofs(t *testing.T) {
	changes := map[string]func(*Proof){
		"no unilateral exit": func(p *Proof) { p.Options.ExitKs = []int{2} },
		"root":               func(p *Proof) { p.Root.Hash[0] ^= 1 }, "sum": func(p *Proof) { p.Root.Sum++ }, "sibling": func(p *Proof) { p.Path[0].Sibling.Hash[0] ^= 1 }, "direction": func(p *Proof) { p.Path[0].Dir = !p.Path[0].Dir }, "length": func(p *Proof) { p.Path = p.Path[1:] }, "position": func(p *Proof) { p.Position = 16 }, "depth": func(p *Proof) { p.Depth = 100 }, "sparse": func(p *Proof) { p.Options.Sparse = true }, "sum overflow": func(p *Proof) { p.Path[0].Sibling.Sum = ^uint64(0) }, "invalid owner": func(p *Proof) { l := *p.Leaf; l.OwnerKey = [32]byte{}; p.Leaf = &l }, "detach": func(p *Proof) { p.Options.DetachHeights = []int{1} }, "huge cohort": func(p *Proof) { p.Options.RewriteHeights = []int{30} },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			f := fixtureState(t)
			p := proofAt(f.st, 2)
			mutate(p)
			if err := p.Validate(); err == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
}

func TestStateFilesAndProposalValidation(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "wallet.json")
	if err = s.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("wallet not private")
	}
	restored, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Secret != s.Secret {
		t.Fatal("wallet did not round trip")
	}
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(status)
	if bytes.Contains(data, []byte(s.Secret)) {
		t.Fatal("status exposed private key")
	}
	f := fixtureState(t)
	s.Proof = proofAt(f.st, 2)
	if err = s.Validate(); err == nil {
		t.Fatal("another owner's leaf accepted")
	}
	s.Proof = nil
	s.Version = 2
	if err = s.Save(path); err == nil {
		t.Fatal("unsupported version accepted")
	}
	if err = os.WriteFile(path, []byte(`{"version":1,"unknown":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path); err == nil {
		t.Fatal("unknown state fields accepted")
	}
	p := proofAt(f.st, 2)
	spend, err := p.Build("EXIT_1", nil, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	proposalPath := filepath.Join(dir, "proposal.json")
	if err = WriteSpend(proposalPath, spend); err != nil {
		t.Fatal(err)
	}
	read, err := ReadSpend(proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	if read.Tx.TxHash() != spend.Tx.TxHash() {
		t.Fatal("proposal did not round trip")
	}
	var raw proposal
	if err = ReadJSON(proposalPath, &raw); err != nil {
		t.Fatal(err)
	}
	raw.Sigs[0].Index = -1
	if err = WriteJSON(proposalPath, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadSpend(proposalPath); err == nil {
		t.Fatal("invalid signature slot accepted")
	}
}

func TestSyncCheckpointFailurePreservesState(t *testing.T) {
	f := fixtureState(t)
	s := &State{Version: 1, Secret: hex.EncodeToString(f.keys[1].Serialize()), Proof: proofAt(f.st, 2), Height: 100, BlockHash: chainhash.Hash{1}}
	original, _ := json.Marshal(s)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any     `json:"id"`
			Method string  `json:"method"`
			Params []int64 `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method != "getblockhash" || len(request.Params) != 1 {
			t.Errorf("unexpected RPC: %s", request.Method)
			http.Error(w, "unexpected", 500)
			return
		}
		hash := chaincfg.RegressionNetParams.GenesisHash.String()
		if request.Params[0] != 0 {
			hash = (chainhash.Hash{2}).String()
		}
		json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": hash, "error": nil})
	}))
	defer server.Close()
	rpc, err := rpcclient.New(&rpcclient.ConnConfig{Host: strings.TrimPrefix(server.URL, "http://"), User: "test", Pass: "test", HTTPPostMode: true, DisableTLS: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Shutdown()
	if _, err = s.Sync(rpc); err == nil || !strings.Contains(err.Error(), "checkpoint reorganized") {
		t.Fatalf("expected reorg rejection: %v", err)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(original, after) {
		t.Fatal("failed sync modified state")
	}
}

func TestProposalPinsTransaction(t *testing.T) {
	f := fixtureState(t)
	spend, err := proofAt(f.st, 2).Build("EXIT_1", nil, f.opts)
	if err != nil {
		t.Fatal(err)
	}
	s := &State{Pending: &Pending{TxID: spend.Tx.TxHash()}}
	spend.Tx.TxOut[1].Value--
	if _, err = s.Submit(nil, spend); err == nil || !strings.Contains(err.Error(), "saved proposal") {
		t.Fatalf("changed transaction was not rejected: %v", err)
	}
}
