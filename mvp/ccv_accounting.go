//go:build layertree

package mvp

import (
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// checkCCVAccounting proves that the node applies CCV's output counters
// across inputs, both in admission and in the valid transaction it mines.
func checkCCVAccounting(rpc *rpcclient.Client, op *actor, cb *wire.MsgTx) error {
	type covenant struct{ script, control, spk []byte }
	var covenants []covenant
	for _, mode := range []byte{txscript.OP_0, txscript.OP_2} {
		script := append([]byte{txscript.OP_0, txscript.OP_0, 32}, op.x[:]...)
		script = append(script, txscript.OP_0, mode, txscript.OP_LAYERTREE_CCV, txscript.OP_TRUE)
		leaf := txscript.NewTapLeaf(txscript.LayerTreeLeafVersion, script)
		root := leaf.TapHash()
		key := txscript.ComputeTaprootOutputKey(op.key.PubKey(), root[:])
		spk, err := txscript.PayToTaprootScript(key)
		if err != nil {
			return err
		}
		control := txscript.ControlBlock{InternalKey: op.key.PubKey(), OutputKeyYIsOdd: key.SerializeCompressed()[0] == 3, LeafVersion: txscript.LayerTreeLeafVersion}
		raw, err := control.ToBytes()
		if err != nil {
			return err
		}
		covenants = append(covenants, covenant{script, raw, spk})
	}
	funding := wire.NewMsgTx(2)
	old := wire.OutPoint{Hash: cb.TxHash(), Index: 0}
	funding.AddTxIn(&wire.TxIn{PreviousOutPoint: old, Sequence: 0xfffffffd})
	for _, c := range covenants {
		funding.AddTxOut(wire.NewTxOut(700, c.spk))
		funding.AddTxOut(wire.NewTxOut(700, c.spk))
	}
	funding.AddTxOut(wire.NewTxOut(cb.TxOut[0].Value-2800-fee, op.spk))
	if err := signWallet(funding, 0, op, map[wire.OutPoint]*wire.TxOut{old: cb.TxOut[0]}); err != nil {
		return err
	}
	if _, err := rpc.SendRawTransaction(funding, false); err != nil {
		return err
	}
	if _, err := rpc.Generate(1); err != nil {
		return err
	}
	var valid *wire.MsgTx
	for j, c := range covenants {
		tx := wire.NewMsgTx(2)
		prevs := map[wire.OutPoint]*wire.TxOut{}
		for k := 0; k < 2; k++ {
			idx := uint32(j*2 + k)
			outpoint := wire.OutPoint{Hash: funding.TxHash(), Index: idx}
			tx.AddTxIn(&wire.TxIn{PreviousOutPoint: outpoint, Sequence: 0xfffffffd, Witness: wire.TxWitness{c.script, c.control}})
			prevs[outpoint] = funding.TxOut[idx]
		}
		feePoint := wire.OutPoint{Hash: funding.TxHash(), Index: 4}
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: feePoint, Sequence: 0xfffffffd})
		prevs[feePoint] = funding.TxOut[4]
		payout := int64(1000)
		if j == 1 {
			payout = 500
		}
		tx.AddTxOut(wire.NewTxOut(payout, append([]byte{txscript.OP_1, 32}, op.x[:]...)))
		tx.AddTxOut(wire.NewTxOut(funding.TxOut[4].Value-fee, op.spk))
		if err := signWallet(tx, 2, op, prevs); err != nil {
			return err
		}
		_, err := rpc.SendRawTransaction(tx, false)
		needle := "below assigned amount"
		if j == 1 {
			needle = "duplicate deduction"
		}
		if err == nil || !strings.Contains(err.Error(), needle) {
			return fmt.Errorf("CCV accounting attack did not fail at %s: %v", needle, err)
		}
		if j == 0 {
			tx.TxOut[0].Value = 1400
			if err := signWallet(tx, 2, op, prevs); err != nil {
				return err
			}
			valid = tx
		}
	}
	hash, err := rpc.SendRawTransaction(valid, false)
	if err != nil {
		return err
	}
	if _, err = rpc.Generate(1); err != nil {
		return err
	}
	confirmed, err := rpc.GetRawTransactionVerbose(hash)
	if err != nil {
		return err
	}
	if confirmed.Confirmations == 0 {
		return fmt.Errorf("CCV shared assignment was not mined")
	}
	return nil
}
