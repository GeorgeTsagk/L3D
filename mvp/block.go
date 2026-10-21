//go:build layertree

package mvp

import (
	"fmt"
	"strings"
	"time"

	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// rejectBlock bypasses the mempool entirely. A correctly mined block with a
// valid witness commitment must still fail the covenant in block validation.
func (d *demo) rejectBlock(s *txn.Spend) error {
	best, height, err := d.rpc.GetBestBlock()
	if err != nil {
		return err
	}
	parent, err := d.rpc.GetBlockHeader(best)
	if err != nil {
		return err
	}
	script, err := txscript.NewScriptBuilder().AddInt64(int64(height + 1)).AddData([]byte("layertree-negative-block")).Script()
	if err != nil {
		return err
	}
	cb := wire.NewMsgTx(2)
	cb.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Index: 0xffffffff}, SignatureScript: script, Sequence: 0xffffffff, Witness: wire.TxWitness{make([]byte, 32)}})
	cb.AddTxOut(wire.NewTxOut(blockchain.CalcBlockSubsidy(height+1,
		&chaincfg.RegressionNetParams), d.actors["operator"].spk))
	txs := []*btcutil.Tx{btcutil.NewTx(cb), btcutil.NewTx(s.Tx)}
	witnessRoot := blockchain.CalcMerkleRoot(txs, true)
	commitment := chainhash.DoubleHashB(append(witnessRoot[:], make([]byte, 32)...))
	cb.AddTxOut(wire.NewTxOut(0, append(append([]byte{}, blockchain.WitnessMagicBytes...), commitment...)))
	timestamp := time.Now().Truncate(time.Second)
	if !timestamp.After(parent.Timestamp) {
		timestamp = parent.Timestamp.Add(time.Second)
	}
	block := wire.NewMsgBlock(&wire.BlockHeader{Version: 0x20000004, PrevBlock: *best, MerkleRoot: blockchain.CalcMerkleRoot(txs, false), Timestamp: timestamp, Bits: parent.Bits})
	block.AddTransaction(cb)
	block.AddTransaction(s.Tx)
	target := blockchain.CompactToBig(block.Header.Bits)
	for {
		hash := block.BlockHash()
		if blockchain.HashToBig(&hash).Cmp(target) <= 0 {
			break
		}
		block.Header.Nonce++
	}
	err = d.rpc.SubmitBlock(btcutil.NewBlock(block), nil)
	if err == nil {
		return fmt.Errorf("invalid covenant accepted inside a block")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "script") {
		return fmt.Errorf("block failed for an unexpected reason: %w", err)
	}
	d.report.Rejections["block-consensus-wrong-remainder"] = err.Error()
	fmt.Fprintln(d.cfg.Output, "REJECT block-consensus-wrong-remainder")
	got, _, err := d.rpc.GetBestBlock()
	if err != nil {
		return err
	}
	if *got != *best {
		return fmt.Errorf("invalid block changed the chain tip")
	}
	return nil
}
