package app

import "testing"

func TestKeeperDaemonSkipsWhenManualRunIsRunning(t *testing.T) {
	runner := NewKeeperRunner(nil)

	if !runner.markRunning("once") {
		t.Fatal("markRunning once = false, want true")
	}
	if runner.markRunning("daemon") {
		t.Fatal("markRunning daemon = true, want false while manual run is running")
	}

	status := runner.Status()
	if !status.Running {
		t.Fatal("status.Running = false, want true")
	}
	if status.Mode == nil || *status.Mode != "once" {
		t.Fatalf("status.Mode = %v, want once", status.Mode)
	}
}

func TestKeeperRunnerAllowsIndependentRefreshModes(t *testing.T) {
	runner := NewKeeperRunner(nil)

	if !runner.markRunning("daemon") {
		t.Fatal("markRunning daemon = false, want true")
	}
	if !runner.markRunning("conditional") {
		t.Fatal("markRunning conditional = false, want true")
	}
	if !runner.markRunning("accounts") {
		t.Fatal("markRunning accounts = false, want true")
	}
	if runner.markRunning("daemon") {
		t.Fatal("markRunning duplicate daemon = true, want false")
	}
	if runner.markRunning("once") {
		t.Fatal("markRunning once = true, want false while daemon is running")
	}

	status := runner.Status()
	if !status.Running {
		t.Fatal("status.Running = false, want true")
	}
	if status.Mode == nil || *status.Mode != "daemon" {
		t.Fatalf("status.Mode = %v, want daemon", status.Mode)
	}
	wantModes := []string{"daemon", "conditional", "accounts"}
	if len(status.RunningModes) != len(wantModes) {
		t.Fatalf("len(status.RunningModes) = %d, want %d", len(status.RunningModes), len(wantModes))
	}
	for index, wantMode := range wantModes {
		if status.RunningModes[index] != wantMode {
			t.Fatalf("status.RunningModes[%d] = %s, want %s", index, status.RunningModes[index], wantMode)
		}
	}
}

func TestKeeperRunnerLocksInFlightAuthNames(t *testing.T) {
	runner := NewKeeperRunner(nil)

	if !runner.tryLockAuthName("daemon", "same.json") {
		t.Fatal("tryLockAuthName daemon same.json = false, want true")
	}
	if runner.tryLockAuthName("conditional", "same.json") {
		t.Fatal("tryLockAuthName conditional same.json = true, want false while auth is in flight")
	}
	if !runner.tryLockAuthName("conditional", "other.json") {
		t.Fatal("tryLockAuthName conditional other.json = false, want true")
	}

	runner.unlockAuthName("same.json")
	if !runner.tryLockAuthName("accounts", "same.json") {
		t.Fatal("tryLockAuthName accounts same.json = false, want true after unlock")
	}
}
