package protocol

import (
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"reflect"
	"testing"
)

func TestModesAndParameters(t *testing.T) {
	if len(Modes()) != 9 {
		t.Fatal("unexpected supported mode count")
	}
	_, pk := btcec.PrivKeyFromBytes([]byte{1})
	var key [32]byte
	copy(key[:], schnorr.SerializePubKey(pk))
	for _, m := range Modes() {
		t.Run(m.Tier+m.Profile, func(t *testing.T) {
			d := m.MinDepth
			if d < 4 {
				d = 4
			}
			c, err := NewConfig(m.Tier, m.Profile, d, key, [32]byte{1})
			if err != nil {
				t.Fatal(err)
			}
			o, err := c.Options()
			if err != nil {
				t.Fatal(err)
			}
			back, err := FromOptions(o)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c, back) {
				t.Fatalf("mode parameters changed: %+v %+v", c, back)
			}
			if err = c.Validate(m.MaxDepth + 1); err == nil {
				t.Fatal("excessive depth accepted")
			}
			if m.Tier != "min" {
				c.Rules.ExitKs = nil
				if err = c.Validate(d); err == nil {
					t.Fatal("tree without unilateral exit accepted")
				}
			}
		})
	}
	if _, err := NewConfig("min", "sparse", 31, key, [32]byte{}); err == nil {
		t.Fatal("MIN sparse invented")
	}
	if _, err := NewConfig("unknown", "dense", 4, key, [32]byte{}); err == nil {
		t.Fatal("unknown tier accepted")
	}
	if _, err := NewConfig("mid", "unknown", 4, key, [32]byte{}); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
