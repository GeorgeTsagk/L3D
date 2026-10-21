//go:build layertree

package lite

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/GeorgeTsagk/L3D/protocol"
	"github.com/GeorgeTsagk/L3D/script"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type Coin struct {
	OutPoint wire.OutPoint `json:"outpoint"`
	Output   wire.TxOut    `json:"output"`
}
type Pending struct {
	TxID chainhash.Hash `json:"txid"`
	Leaf *tree.Leaf     `json:"leaf"`
}
type State struct {
	Version   int            `json:"version"`
	Secret    string         `json:"secret"`
	Proof     *Proof         `json:"proof,omitempty"`
	Height    int64          `json:"height"`
	BlockHash chainhash.Hash `json:"block_hash"`
	Pending   *Pending       `json:"pending,omitempty"`
	Payout    *Coin          `json:"payout,omitempty"`
}
type Status struct {
	Mode           *protocol.Capabilities `json:"mode,omitempty"`
	Position       uint64                 `json:"position,omitempty"`
	Owner          string                 `json:"owner"`
	FundingAddress string                 `json:"funding_address"`
	FundingScript  string                 `json:"funding_script"`
	Balance        uint64                 `json:"balance_sat"`
	State          string                 `json:"state"`
	Height         int64                  `json:"height"`
	OutPoint       string                 `json:"outpoint,omitempty"`
	Pending        string                 `json:"pending,omitempty"`
	Payout         *Coin                  `json:"payout,omitempty"`
}

func New() (*State, error) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	return &State{Version: 1, Secret: hex.EncodeToString(key.Serialize())}, nil
}
func (s *State) key() (*btcec.PrivateKey, error) {
	raw, err := hex.DecodeString(s.Secret)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("invalid wallet secret")
	}
	var scalar btcec.ModNScalar
	if scalar.SetByteSlice(raw) || scalar.IsZero() {
		return nil, fmt.Errorf("invalid wallet scalar")
	}
	key, _ := btcec.PrivKeyFromBytes(raw)
	return key, nil
}
func (s *State) identity() ([32]byte, []byte, error) {
	var x [32]byte
	key, err := s.key()
	if err != nil {
		return x, nil, err
	}
	copy(x[:], schnorr.SerializePubKey(key.PubKey()))
	spk, err := txscript.PayToTaprootScript(txscript.ComputeTaprootKeyNoScript(key.PubKey()))
	return x, spk, err
}
func (s *State) Status() (Status, error) {
	x, spk, err := s.identity()
	if err != nil {
		return Status{}, err
	}
	addr, err := btcutil.NewAddressTaproot(spk[2:], &chaincfg.RegressionNetParams)
	if err != nil {
		return Status{}, err
	}
	status := Status{Owner: hex.EncodeToString(x[:]), FundingAddress: addr.EncodeAddress(), FundingScript: hex.EncodeToString(spk), State: "unattached", Height: s.Height, Payout: s.Payout}
	if s.Proof != nil {
		c, err := s.Proof.Parameters()
		if err != nil {
			return status, err
		}
		caps, err := c.Capabilities()
		if err != nil {
			return status, err
		}
		status.Mode = &caps
		status.Position = s.Proof.Position
		status.OutPoint = s.Proof.OutPoint.String()
		status.State = "empty"
		if s.Proof.Leaf != nil {
			status.Balance = s.Proof.Leaf.Amount
			status.State = "active"
		}
	}
	if s.Payout != nil {
		status.State = "exited"
	}
	if s.Pending != nil {
		status.Pending = s.Pending.TxID.String()
	}
	return status, nil
}
func (s *State) Validate() error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported wallet version")
	}
	x, _, err := s.identity()
	if err != nil {
		return err
	}
	if s.Proof != nil {
		if err = s.Proof.Validate(); err != nil {
			return err
		}
		if s.Proof.Leaf != nil && (s.Proof.Leaf.OwnerKey != x || s.Proof.Leaf.ExitKey != x) {
			return fmt.Errorf("proof belongs to another wallet")
		}
	}
	if s.Pending != nil && s.Pending.Leaf != nil && (s.Pending.Leaf.OwnerKey != x || s.Pending.Leaf.ExitKey != x) {
		return fmt.Errorf("pending leaf belongs to another wallet")
	}
	return nil
}

// CheckNetwork prevents this experimental wallet from signing against a
// public network. The RPC backend is trusted for chain selection and UTXOs.
func CheckNetwork(rpc *rpcclient.Client) error {
	hash, err := rpc.GetBlockHash(0)
	if err != nil {
		return err
	}
	if *hash != *chaincfg.RegressionNetParams.GenesisHash {
		return fmt.Errorf("lite MVP requires regtest")
	}
	return nil
}
func UTXO(rpc *rpcclient.Client, op wire.OutPoint, mempool bool) (*wire.TxOut, error) {
	result, err := rpc.GetTxOut(&op.Hash, op.Index, mempool)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("output %s is spent or unknown", op)
	}
	spk, err := hex.DecodeString(result.ScriptPubKey.Hex)
	if err != nil {
		return nil, err
	}
	amount, err := btcutil.NewAmount(result.Value)
	if err != nil {
		return nil, err
	}
	return wire.NewTxOut(int64(amount), spk), nil
}
func prevOutputs(rpc *rpcclient.Client, tx *wire.MsgTx) (map[wire.OutPoint]*wire.TxOut, error) {
	prevs := make(map[wire.OutPoint]*wire.TxOut)
	for _, in := range tx.TxIn {
		op := in.PreviousOutPoint
		raw, err := rpc.GetRawTransaction(&op.Hash)
		if err != nil {
			return nil, fmt.Errorf("get historical transaction, backend needs txindex: %w", err)
		}
		if raw.MsgTx().TxHash() != op.Hash || int(op.Index) >= len(raw.MsgTx().TxOut) {
			return nil, fmt.Errorf("invalid previous transaction")
		}
		prevs[op] = raw.MsgTx().TxOut[op.Index]
	}
	return prevs, nil
}

func (s *State) Attach(rpc *rpcclient.Client, p Proof) error {
	if s.Proof != nil || s.Payout != nil {
		return fmt.Errorf("wallet already has a membership")
	}
	if err := CheckNetwork(rpc); err != nil {
		return err
	}
	tip, height, err := rpc.GetBestBlock()
	if err != nil {
		return err
	}
	out, err := UTXO(rpc, p.OutPoint, false)
	if err != nil {
		return err
	}
	if err = p.CheckOutput(out); err != nil {
		return err
	}
	tx, err := rpc.GetRawTransactionVerbose(&p.OutPoint.Hash)
	if err != nil {
		return err
	}
	if tx.Confirmations == 0 {
		return fmt.Errorf("proof anchor must be confirmed")
	}
	config, _ := p.Parameters()
	if config.Profile == "sparse" && p.Leaf == nil {
		x, _, err := s.identity()
		if err != nil {
			return err
		}
		leaf := &tree.Leaf{OwnerKey: x, Nonce: p.Nonce}
		tier, _ := config.OpcodeTier()
		if leaf.Position(p.Depth) != p.Position || (tier.Mode() == script.ModeC0 && !leaf.PosLoMinimal(p.Depth)) {
			return fmt.Errorf("empty sparse slot is not derived from this wallet and nonce")
		}
	}
	candidate := *s
	candidate.Proof = &p
	candidate.Height = int64(height)
	candidate.BlockHash = *tip
	if err = candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// Sync scans confirmed blocks only while the command runs. It verifies the
// saved checkpoint before replay and never overwrites state on failure.
func (s *State) Sync(rpc *rpcclient.Client) (int, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	if err := CheckNetwork(rpc); err != nil {
		return 0, err
	}
	if s.Proof == nil && s.Payout == nil {
		return 0, fmt.Errorf("wallet is not attached")
	}
	anchor, err := rpc.GetBlockHash(s.Height)
	if err != nil {
		return 0, err
	}
	if *anchor != s.BlockHash {
		return 0, fmt.Errorf("checkpoint reorganized, restore an earlier proof backup")
	}
	tip, height, err := rpc.GetBestBlock()
	if err != nil {
		return 0, err
	}
	candidate := *s
	count := 0
	for h := s.Height + 1; h <= int64(height); h++ {
		hash, err := rpc.GetBlockHash(h)
		if err != nil {
			return 0, err
		}
		block, err := rpc.GetBlock(hash)
		if err != nil {
			return 0, err
		}
		for _, tx := range block.Transactions {
			if candidate.Proof == nil || len(tx.TxIn) == 0 || tx.TxIn[0].PreviousOutPoint != candidate.Proof.OutPoint {
				continue
			}
			prevs, err := prevOutputs(rpc, tx)
			if err != nil {
				return 0, err
			}
			var known *tree.Leaf
			if candidate.Pending != nil {
				known = candidate.Pending.Leaf
			}
			next, payout, err := Advance(candidate.Proof, tx, prevs, known)
			if err != nil {
				return 0, err
			}
			candidate.Proof = next
			candidate.Payout = payout
			// Any confirmed spend of this tree outpoint either confirms
			// our proposal or makes it impossible to confirm later.
			candidate.Pending = nil
			if err = candidate.Validate(); err != nil {
				return 0, err
			}
			count++
		}
	}
	hash, err := rpc.GetBlockHash(int64(height))
	if err != nil {
		return 0, err
	}
	if *hash != *tip {
		return 0, fmt.Errorf("chain changed during synchronization")
	}
	candidate.Height = int64(height)
	candidate.BlockHash = *tip
	if candidate.Proof != nil {
		out, err := UTXO(rpc, candidate.Proof.OutPoint, false)
		if err != nil {
			return 0, err
		}
		if err = candidate.Proof.CheckOutput(out); err != nil {
			return 0, err
		}
	}
	*s = candidate
	return count, nil
}

// Prepare produces a transaction ready for an operator signature when needed.
// The member signs only their owner and fee inputs and persists its txid.
func (s *State) Prepare(rpc *rpcclient.Client, op string, amount uint64, evictAfter uint32, funding wire.OutPoint, fee int64) (*txn.Spend, error) {
	if _, err := s.Sync(rpc); err != nil {
		return nil, err
	}
	if s.Proof == nil {
		return nil, fmt.Errorf("member already exited")
	}
	config, _ := s.Proof.Parameters()
	caps, err := config.Capabilities()
	if err != nil {
		return nil, err
	}
	tier, _ := config.OpcodeTier()
	if (op == "JOIN_0" && !caps.Join) || (op == "UPDATE" && !caps.Update) {
		return nil, fmt.Errorf("%s tier does not support %s", config.Tier, op)
	}
	// A withheld cooperative signature must never block an owner's exit.
	if s.Pending != nil && op != "EXIT_1" {
		return nil, fmt.Errorf("a transaction is pending confirmation")
	}
	if fee <= 0 || fee > 1000000 {
		return nil, fmt.Errorf("fee outside MVP bounds")
	}
	x, spk, err := s.identity()
	if err != nil {
		return nil, err
	}
	var next *tree.Leaf
	delta := int64(0)
	switch op {
	case "JOIN_0":
		if amount < tree.DustMin || amount > caps.MaxSum {
			return nil, fmt.Errorf("join amount outside bounds")
		}
		next = &tree.Leaf{Amount: amount, OwnerKey: x, ExitKey: x, EvictorKey: config.Rules.OperatorKey, EvictAfter: evictAfter, Seq: 1, Nonce: s.Proof.Nonce}
		delta = int64(amount)
	case "UPDATE":
		if s.Proof.Leaf == nil {
			return nil, fmt.Errorf("no leaf to update")
		}
		if amount < tree.DustMin || amount > caps.MaxSum {
			return nil, fmt.Errorf("update amount outside bounds")
		}
		l := *s.Proof.Leaf
		if l.Seq >= tier.SeqFinal() {
			return nil, fmt.Errorf("sequence limit reached")
		}
		l.Amount = amount
		l.EvictAfter = evictAfter
		l.Seq++
		next = &l
		delta = int64(amount) - int64(s.Proof.Leaf.Amount)
	case "EXIT_1":
	default:
		return nil, fmt.Errorf("unknown operation")
	}
	if next != nil && next.EvictAfter <= uint32(s.Height) {
		return nil, fmt.Errorf("lease must expire in the future")
	}
	prev, err := UTXO(rpc, funding, true)
	if err != nil {
		return nil, err
	}
	if funding == s.Proof.OutPoint || !bytes.Equal(prev.PkScript, spk) {
		return nil, fmt.Errorf("funding output is not this wallet's")
	}
	change := prev.Value - delta - fee
	if change < 330 {
		return nil, fmt.Errorf("insufficient funding or dust change")
	}
	spend, err := s.Proof.Build(op, next, txn.TxOpts{Inputs: []txn.ExtraInput{{OutPoint: funding, PrevOut: prev, Sequence: txn.TreeSequence}}, Outputs: []*wire.TxOut{wire.NewTxOut(change, spk)}})
	if err != nil {
		return nil, err
	}
	key, err := s.key()
	if err != nil {
		return nil, err
	}
	hash, err := spend.Sighash()
	if err != nil {
		return nil, err
	}
	for _, slot := range spend.Sigs {
		if slot.Key != x {
			continue
		}
		sig, err := txn.SignSchnorr(key, hash)
		if err != nil {
			return nil, err
		}
		spend.Stack[slot.Index] = sig
	}
	if err = AttachWitness(spend); err != nil {
		return nil, err
	}
	fetch := txscript.NewMultiPrevOutFetcher(spend.PrevOuts)
	hashes := txscript.NewTxSigHashes(spend.Tx, fetch)
	digest, err := txscript.CalcTaprootSignatureHash(hashes, txscript.SigHashDefault, spend.Tx, 1, fetch)
	if err != nil {
		return nil, err
	}
	sig, err := schnorr.Sign(txscript.TweakTaprootPrivKey(*key, nil), digest)
	if err != nil {
		return nil, err
	}
	spend.Tx.TxIn[1].Witness = wire.TxWitness{sig.Serialize()}
	s.Pending = &Pending{TxID: spend.Tx.TxHash(), Leaf: next}
	return spend, nil
}

func AttachWitness(s *txn.Spend) error {
	return txn.TapscriptSpend(s.Tx, 0, s.PrevOuts, s.Leaf.Script, s.Leaf.Version, s.ControlBlock, s.Stack)
}

func (s *State) Submit(rpc *rpcclient.Client, spend *txn.Spend) (*chainhash.Hash, error) {
	if s.Pending == nil || s.Pending.TxID != spend.Tx.TxHash() {
		return nil, fmt.Errorf("transaction is not the saved proposal")
	}
	if _, err := s.Sync(rpc); err != nil {
		return nil, err
	}
	if s.Proof == nil || len(spend.Tx.TxIn) != 2 || spend.Tx.TxIn[0].PreviousOutPoint != s.Proof.OutPoint {
		return nil, fmt.Errorf("proposal is stale")
	}
	prevs := make(map[wire.OutPoint]*wire.TxOut)
	for _, in := range spend.Tx.TxIn {
		out, err := UTXO(rpc, in.PreviousOutPoint, true)
		if err != nil {
			return nil, err
		}
		prevs[in.PreviousOutPoint] = out
	}
	fetch := txscript.NewMultiPrevOutFetcher(prevs)
	if err := txscript.ValidateLayerTreeTransaction(spend.Tx, fetch, txscript.StandardVerifyFlags|txscript.ScriptVerifyLayerTree, nil, nil); err != nil {
		return nil, err
	}

	return rpc.SendRawTransaction(spend.Tx, false)
}

// Locate returns a candidate sparse slot without contacting an operator.
// The operator still checks vacancy and provides the authenticated path.
func (s *State) Locate(tier, profile string, depth int) (uint64, uint32, error) {
	return s.LocateFrom(tier, profile, depth, 0)
}

// LocateFrom retries allocation starting from a caller-selected nonce.
func (s *State) LocateFrom(tier, profile string, depth int, start uint32) (uint64, uint32, error) {
	c := protocol.Config{Tier: tier, Profile: profile}
	caps, err := c.Capabilities()
	if err != nil {
		return 0, 0, err
	}
	if profile != "sparse" {
		return 0, 0, fmt.Errorf("dense slots are allocated by the operator")
	}
	if depth < caps.MinDepth || depth > caps.MaxDepth {
		return 0, 0, fmt.Errorf("depth outside supported range")
	}
	x, _, err := s.identity()
	if err != nil {
		return 0, 0, err
	}
	t, _ := c.OpcodeTier()
	leaf := &tree.Leaf{OwnerKey: x}
	for n := uint64(start); n < uint64(start)+(1<<24) && n <= 0xffffffff; n++ {
		leaf.Nonce = uint32(n)
		if t.Mode() != script.ModeC0 || leaf.PosLoMinimal(depth) {
			return leaf.Position(depth), leaf.Nonce, nil
		}
	}
	return 0, 0, fmt.Errorf("sparse nonce search exhausted")
}
