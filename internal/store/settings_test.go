package store

import (
	"context"
	"testing"
)

func TestSettings(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, ok, err := db.Setting(ctx, SettingPolling); err != nil || ok {
		t.Fatalf("unset setting = ok %v, err %v", ok, err)
	}
	if err := db.SetSetting(ctx, SettingPolling, "5m", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, SettingPolling, "manual", at(1)); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := db.Setting(ctx, SettingPolling); err != nil || !ok || v != "manual" {
		t.Fatalf("Setting = %q, %v, %v", v, ok, err)
	}
	if err := db.DeleteSetting(ctx, SettingPolling); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.Setting(ctx, SettingPolling); ok {
		t.Fatal("deleted setting still present")
	}
	if err := db.DeleteSetting(ctx, SettingPolling); err != nil {
		t.Fatalf("deleting an unset setting: %v", err)
	}
}
