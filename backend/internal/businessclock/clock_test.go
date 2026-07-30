package businessclock

import "testing"

func TestLocationLoadsEmbeddedShanghaiTimezone(t *testing.T) {
	t.Setenv("ZONEINFO", t.TempDir()+"/missing-zoneinfo.zip")
	location, err := Location()
	if err != nil {
		t.Fatalf("Location returned error: %v", err)
	}
	if location.String() != "Asia/Shanghai" {
		t.Fatalf("Location = %q", location)
	}
}
