//go:build layertree

package mvp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegtestLifecycle(t *testing.T) {
	binary := os.Getenv("LAYERTREE_BTCD")
	if binary == "" {
		t.Skip("set LAYERTREE_BTCD to the patched btcd binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	report, err := Run(ctx, Config{BTCD: binary, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Success || len(report.Events) != 12 || len(report.Rejections) != 7 {
		t.Fatalf("incomplete report: %+v", report)
	}
	for _, ev := range report.Events {
		if ev.Fee != fee || ev.VBytes <= 0 {
			t.Fatalf("invalid transaction accounting: %+v", ev)
		}
	}
	want := map[string]uint64{"alice": 160000, "bob": 115000, "carol": 100000, "dave": 100000, "eve": 25000, "operator": 1000000}
	got := report.Events[6].Balances
	for name, amount := range want {
		if got[name] != amount {
			t.Fatalf("claim balance %s=%d, want %d", name, got[name], amount)
		}
	}
	last := report.Events[len(report.Events)-1]
	if last.Operation != "REDEEM-payouts" || len(last.Balances) != 1 || last.Balances["operator"] != 1000000 {
		t.Fatalf("wrong final state: %+v", last)
	}
	var saved Report
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Success || saved.FinalTreeOutpoint != report.FinalTreeOutpoint {
		t.Fatal("report was not persisted")
	}
	if _, err = Run(ctx, Config{BTCD: binary, Dir: dir}); err == nil {
		t.Fatal("existing artifacts overwritten")
	}
}

func TestRegtestLiteClient(t *testing.T) {
	binary, client := os.Getenv("LAYERTREE_BTCD"), os.Getenv("LAYERTREE_LITECLI")
	if binary == "" || client == "" {
		t.Skip("set LAYERTREE_BTCD and LAYERTREE_LITECLI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	report, err := RunLite(ctx, LiteConfig{BTCD: binary, Client: client, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Success || report.ClientInvocations != 16 || report.OfflineBlocks != 148 || report.ExitAmount != 160000 || !report.EvictionObserved || report.OperatorBalance != 1000000 {
		t.Fatalf("incomplete lite demo: %+v", report)
	}
	if report.ActiveStateBytes <= 500 || report.ActiveStateBytes > 20000 {
		t.Fatalf("unexpected active wallet size %d", report.ActiveStateBytes)
	}
}

func TestRegtestAllModes(t *testing.T) {
	binary, client := os.Getenv("LAYERTREE_BTCD"), os.Getenv("LAYERTREE_LITECLI")
	if binary == "" || client == "" {
		t.Skip("set LAYERTREE_BTCD and LAYERTREE_LITECLI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	report, err := RunModes(ctx, LiteConfig{BTCD: binary, Client: client, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Success || len(report.Modes) != 9 || !report.CCVAccounting {
		t.Fatalf("incomplete modes report: %+v", report)
	}
	for _, r := range report.Modes {
		if !r.Success || r.OfflineBlocks < 11 || r.ExitVB <= 0 || r.StateBytes < 500 || r.StateBytes > 20000 {
			t.Fatalf("incomplete mode result: %+v", r)
		}
		if r.Mode == "min" {
			if !r.JoinedAtCreation || r.JoinVB != 0 || r.UpdateVB != 0 || r.ExitAmount != 100000 {
				t.Fatalf("invalid MIN capabilities: %+v", r)
			}
		} else {
			if r.JoinedAtCreation || r.JoinVB <= 0 || r.UpdateVB <= 0 || r.DecreaseVB <= 0 || r.ExitAmount != 140000 {
				t.Fatalf("incomplete lifecycle: %+v", r)
			}
			if r.Profile == "dense" && (r.BatchJoins != 4 || r.BatchUpdates != 4 || r.BatchExits != 4) {
				t.Fatalf("missing batches: %+v", r)
			}
		}
	}
}
