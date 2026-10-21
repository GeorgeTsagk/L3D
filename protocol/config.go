// Package protocol describes a LayerTree deployment independently of its
// operator, member wallet, and Bitcoin backend.
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GeorgeTsagk/L3D/script"
	"github.com/GeorgeTsagk/L3D/tree"
	"github.com/GeorgeTsagk/L3D/txn"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

type Config struct {
	Tier    string                `json:"tier"`
	Profile string                `json:"profile"`
	Rules   script.MidTreeOptions `json:"rules"`
	Exact   bool                  `json:"exact,omitempty"`
}

type Capabilities struct {
	Tier     string `json:"tier"`
	Profile  string `json:"profile"`
	Join     bool   `json:"join"`
	Update   bool   `json:"update"`
	Exit     bool   `json:"exit"`
	Cohorts  bool   `json:"cohorts"`
	MaxSum   uint64 `json:"max_sum_sat"`
	MinDepth int    `json:"min_depth"`
	MaxDepth int    `json:"max_depth"`
}

func (c Config) OpcodeTier() (txn.Tier, error) {
	switch c.Tier {
	case "full":
		return txn.Full, nil
	case "mid":
		return txn.Mid, nil
	case "min":
		return txn.Min, nil
	case "ccv":
		return txn.CCV, nil
	case "ccv0":
		return txn.CCV0, nil
	}
	return 0, fmt.Errorf("unknown opcode tier %q", c.Tier)
}
func (c Config) Capabilities() (Capabilities, error) {
	t, err := c.OpcodeTier()
	if err != nil {
		return Capabilities{}, err
	}
	if c.Profile != "dense" && c.Profile != "sparse" {
		return Capabilities{}, fmt.Errorf("unknown tree profile %q", c.Profile)
	}
	if t == txn.Min && c.Profile == "sparse" {
		return Capabilities{}, fmt.Errorf("MIN reference scripts support only dense trees")
	}
	v := Capabilities{Tier: c.Tier, Profile: c.Profile, Join: t != txn.Min, Update: t != txn.Min, Exit: true, Cohorts: t != txn.Min && c.Profile == "dense", MaxSum: tree.MaxSum - 1, MinDepth: 1, MaxDepth: 63}
	if t.Mode() == script.ModeC0 {
		v.MaxSum = tree.MidTierMaxSum
		v.MaxDepth = 31
		if c.Profile == "sparse" {
			v.MinDepth = 25
		}
	}
	return v, nil
}
func Modes() []Capabilities {
	var out []Capabilities
	for _, tier := range []string{"full", "mid", "min", "ccv", "ccv0"} {
		for _, profile := range []string{"dense", "sparse"} {
			v, err := (Config{Tier: tier, Profile: profile}).Capabilities()
			if err == nil {
				out = append(out, v)
			}
		}
	}
	return out
}

// Options translates one public descriptor into the author's existing
// concrete script option types. Shared fields retain their exact values.
func (c Config) Options() (txn.Options, error) {
	if _, err := c.Capabilities(); err != nil {
		return nil, err
	}
	t, err := c.OpcodeTier()
	if err != nil {
		return nil, err
	}
	r := c.Rules
	r.Sparse = c.Profile == "sparse"
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	switch t {
	case txn.Mid:
		return r, nil
	case txn.Min:
		return script.MinTreeOptions{DustMin: r.DustMin}, nil
	case txn.Full:
		var o script.FullTreeOptions
		if err = json.Unmarshal(b, &o); err != nil {
			return nil, err
		}
		o.Exact = c.Exact
		return o, nil
	default:
		var o script.CCVTreeOptions
		if err = json.Unmarshal(b, &o); err != nil {
			return nil, err
		}
		o.C0 = t == txn.CCV0
		return o, nil
	}
}
func FromOptions(o txn.Options) (Config, error) {
	var c Config
	switch v := o.(type) {
	case script.FullTreeOptions:
		c.Tier = "full"
		c.Exact = v.Exact
	case script.MidTreeOptions:
		c.Tier = "mid"
	case script.MinTreeOptions:
		c.Tier = "min"
	case script.CCVTreeOptions:
		c.Tier = "ccv"
		if v.C0 {
			c.Tier = "ccv0"
		}
	default:
		return c, fmt.Errorf("unknown tree option type")
	}
	b, err := json.Marshal(o)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c.Rules); err != nil {
		return c, err
	}
	c.Profile = "dense"
	if c.Rules.Sparse {
		c.Profile = "sparse"
	}
	return c, nil
}

// NewConfig provides a small useful script set for ordinary member leaves.
// Cohort heights can be extended before the tree is created.
func NewConfig(tier, profile string, depth int, operator, treeID [32]byte) (Config, error) {
	c := Config{Tier: strings.ToLower(tier), Profile: strings.ToLower(profile), Exact: strings.ToLower(tier) == "full"}
	c.Rules = script.MidTreeOptions{OperatorKey: operator, TreeID: treeID, Sparse: c.Profile == "sparse", ExitKs: []int{1, 2}, JoinHeights: []int{0}, JoinKs: []int{2}}
	if c.Tier == "min" {
		c.Rules = script.MidTreeOptions{DustMin: tree.DustMin}
	} else if c.Profile == "dense" {
		for h := 1; h <= depth && h <= 6; h++ {
			c.Rules.JoinHeights = append(c.Rules.JoinHeights, h)
			c.Rules.RewriteHeights = append(c.Rules.RewriteHeights, h)
			c.Rules.EvictHeights = append(c.Rules.EvictHeights, h)
		}
	}
	return c, c.Validate(depth)
}

func (c Config) Validate(depth int) error {
	caps, err := c.Capabilities()
	if err != nil {
		return err
	}
	if depth < caps.MinDepth || depth > caps.MaxDepth {
		return fmt.Errorf("%s %s requires depth %d through %d", c.Tier, c.Profile, caps.MinDepth, caps.MaxDepth)
	}
	o := c.Rules
	if o.Sparse != (c.Profile == "sparse") {
		return fmt.Errorf("profile and sparse rule disagree")
	}
	// These optional constructions require a different wallet state machine.
	if o.Tower || o.UpdateX || o.RewriteX || o.ReclaimKey != nil || len(o.DetachHeights) != 0 {
		return fmt.Errorf("member client does not support tower, exit-consent, reclaim or detach variants")
	}
	if c.Tier == "min" {
		if o.OperatorKey != ([32]byte{}) || o.TreeID != ([32]byte{}) || len(o.ExitKs)+len(o.JoinKs)+len(o.JoinHeights)+len(o.RewriteHeights)+len(o.EvictHeights) != 0 || o.Claim || o.ClaimH || o.ClaimX || o.ClaimHX || o.Split || o.ClaimM || o.ClaimHM {
			return fmt.Errorf("MIN supports only its fixed EXIT_1 and SPLIT script set")
		}
		return nil
	}
	if _, err := schnorr.ParsePubKey(o.OperatorKey[:]); err != nil {
		return fmt.Errorf("invalid operator key: %w", err)
	}
	hasExit := false
	for _, k := range o.ExitKs {
		hasExit = hasExit || k == 1
	}
	if !hasExit {
		return fmt.Errorf("tree must provide unilateral EXIT_1")
	}
	if len(o.JoinHeights)+len(o.ExitKs)+len(o.JoinKs)+len(o.RewriteHeights)+len(o.EvictHeights) > 64 {
		return fmt.Errorf("too many script variants")
	}
	for _, set := range [][]int{o.JoinHeights, o.RewriteHeights, o.EvictHeights} {
		seen := map[int]bool{}
		for _, h := range set {
			if h < 0 || h > depth || h > 8 || seen[h] {
				return fmt.Errorf("invalid or duplicate cohort height")
			}
			seen[h] = true
			if c.Profile == "sparse" && h != 0 {
				return fmt.Errorf("sparse profile has no aligned cohort operations")
			}
		}
	}
	if c.Profile == "sparse" && len(o.RewriteHeights)+len(o.EvictHeights) != 0 {
		return fmt.Errorf("sparse profile has no cohort rewrite or sweep")
	}
	for _, set := range [][]int{o.ExitKs, o.JoinKs} {
		seen := map[int]bool{}
		for _, k := range set {
			if k < 1 || k > 16 || seen[k] {
				return fmt.Errorf("invalid or duplicate batch size")
			}
			seen[k] = true
		}
	}
	return nil
}
