//go:build layertree

package txn

import (
	"testing"

	"github.com/btcsuite/btcd/txscript"
)

// TestAllTiersNodeInterpreter runs the existing reference scenarios through the
// patched node's actual script engine, including sparse and batched forms.
func TestAllTiersNodeInterpreter(t *testing.T) {
	count := 0
	for _, c := range allCases() {
		count++
		t.Run(c.key(), func(t *testing.T) {
			w := newWorld(t, c.caseSpec)
			s, next, err := c.build(w)
			if err != nil {
				t.Fatal(err)
			}
			checkSpend(t, w, w.st.Tree, s, next)
			if c.tamper != nil {
				c.tamper(s)
				if err := TapscriptSpend(s.Tx, 0, s.PrevOuts, s.Leaf.Script, s.Leaf.Version, s.ControlBlock, s.Stack); err != nil {
					t.Fatal(err)
				}
			}
			fetcher := txscript.NewMultiPrevOutFetcher(s.PrevOuts)
			prev := s.PrevOuts[s.Tx.TxIn[0].PreviousOutPoint]
			vm, err := txscript.NewEngine(prev.PkScript, s.Tx, 0,
				txscript.StandardVerifyFlags|txscript.ScriptVerifyLayerTree,
				nil, txscript.NewTxSigHashes(s.Tx, fetcher), prev.Value, fetcher)
			if err == nil {
				err = vm.Execute()
			}
			if c.expectFail && err == nil {
				t.Fatal("invalid reference witness accepted by node")
			}
			if !c.expectFail && err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Logf("executed %d reference scenarios across all tiers", count)
}
