//go:build layertree

package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/GeorgeTsagk/L3D/lite"
)

// clientConfig holds persistent CLI defaults. Transaction amounts, funding
// inputs and proposal paths remain explicit per-command flags.
type clientConfig struct {
	Network   string `json:"network"`
	State     string `json:"state"`
	RPCConfig string `json:"rpc_config"`
	Tier      string `json:"tier"`
	Profile   string `json:"profile"`
	Depth     int    `json:"depth"`
	Fee       int64  `json:"fee_sat"`
}

func applyConfig(fs *flag.FlagSet, path string) error {
	if path == "" {
		return validateConfigFlags(fs)
	}
	c := clientConfig{Network: "regtest", Depth: 31, Fee: 20000}
	if err := lite.ReadJSON(path, &c); err != nil {
		return fmt.Errorf("client config: %w", err)
	}
	if c.Network != "regtest" {
		return fmt.Errorf("client config: only regtest is supported")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return err
	}
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	values := map[string]string{
		"state":      resolve(c.State),
		"rpc-config": resolve(c.RPCConfig),
		"tier":       c.Tier,
		"profile":    c.Profile,
		"depth":      strconv.Itoa(c.Depth),
		"fee":        strconv.FormatInt(c.Fee, 10),
	}
	// Explicit flags take precedence, including zero and empty values.
	fs.Visit(func(f *flag.Flag) { delete(values, f.Name) })
	for name, value := range values {
		if err := fs.Set(name, value); err != nil {
			return err
		}
	}
	return validateConfigFlags(fs)
}

func validateConfigFlags(fs *flag.FlagSet) error {
	tier, profile := fs.Lookup("tier").Value.String(), fs.Lookup("profile").Value.String()
	switch tier {
	case "", "full", "mid", "min", "ccv", "ccv0":
	default:
		return fmt.Errorf("client config: unknown tier %q", tier)
	}
	switch profile {
	case "", "dense", "sparse":
	default:
		return fmt.Errorf("client config: unknown profile %q", profile)
	}
	if tier == "min" && profile == "sparse" {
		return fmt.Errorf("client config: MIN supports dense trees only")
	}
	depth, err := strconv.Atoi(fs.Lookup("depth").Value.String())
	if err != nil || depth < 1 || depth > 63 {
		return fmt.Errorf("client config: depth must be between 1 and 63")
	}
	if profile == "sparse" && (tier == "mid" || tier == "ccv0") && (depth < 25 || depth > 31) {
		return fmt.Errorf("client config: C0 sparse depth must be between 25 and 31")
	}
	fee, err := strconv.ParseInt(fs.Lookup("fee").Value.String(), 10, 64)
	if err != nil || fee < 0 {
		return fmt.Errorf("client config: fee must be nonnegative")
	}
	return nil
}
