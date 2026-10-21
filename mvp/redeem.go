//go:build layertree

package mvp

import (
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type ownedCoin struct {
	owner *actor
	coin  coin
}

// redeem spends the exit outputs with their owners' raw exit keys, proving
// that each payout is usable as ordinary Bitcoin after leaving the tree.
func (d *demo) redeem(payouts []ownedCoin) error {
	tx := wire.NewMsgTx(2)
	prevs := make(map[wire.OutPoint]*wire.TxOut)
	for i, p := range payouts {
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: p.coin.op, Sequence: txn.TreeSequence})
		amount := p.coin.out.Value
		if i == 0 {
			amount -= fee
		}
		tx.AddTxOut(wire.NewTxOut(amount, p.owner.spk))
		prevs[p.coin.op] = p.coin.out
	}
	fetch := txscript.NewMultiPrevOutFetcher(prevs)
	hashes := txscript.NewTxSigHashes(tx, fetch)
	for i, p := range payouts {
		digest, err := txscript.CalcTaprootSignatureHash(hashes, txscript.SigHashDefault, tx, i, fetch)
		if err != nil {
			return err
		}
		sig, err := schnorr.Sign(p.owner.key, digest)
		if err != nil {
			return err
		}
		tx.TxIn[i].Witness = wire.TxWitness{sig.Serialize()}
	}
	return d.confirm("REDEEM-payouts", tx, prevs, nil)
}
