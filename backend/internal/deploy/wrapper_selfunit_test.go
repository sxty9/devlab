package deploy

// The self install keeps devlabd's OWN unit current from the delivered, checked-in copy. A self UPDATE
// otherwise replaces only the binary and never rewrites the unit, so a SECURITY directive the template
// gained (SupplementaryGroups=… systemd-journal — the right the honest gate needs to READ a failed
// start's journal and name its cause) would sit in the repo and never take effect on the running service.
// This pins that the refresh is planned when the delivered unit differs, refused when foreign, and
// skipped when the installed unit already matches.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// selfDeliveredUnit is a plausible devlabd.service carrying the journal-group directive and User=devlab.
const selfDeliveredUnit = "[Unit]\nDescription=DevLab backend (devlabd)\n[Service]\nUser=devlab\n" +
	"SupplementaryGroups=holistic systemd-journal\nExecStart=/usr/local/bin/devlabd\n" +
	"Environment=DEVLAB_ADDR=127.0.0.1:8781\n"

// selfUnitFixture extends selfFixture with a delivered setup/devlabd.service and a temp unit dir + fake
// systemd so the first-time-vs-update-vs-foreign decision runs off the fixture, not the host.
func selfUnitFixture(t *testing.T, deliveredUnit, installedUnit, fragment string) (env map[string]string, artifact string) {
	t.Helper()
	env, artifact = selfFixture(t, "devlab", testOwner)
	setup := filepath.Join(artifact, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setup, "devlabd.service"), []byte(deliveredUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	unitDir := t.TempDir()
	if installedUnit != "" {
		if err := os.WriteFile(filepath.Join(unitDir, "devlabd.service"), []byte(installedUnit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env["DEVLAB_UNIT_DIR"] = unitDir
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, fragment)
	return env, artifact
}

// A devlabd installed before the systemd-journal directive: the installed unit differs from the delivered
// one, so the self install refreshes it and reloads systemd, and the handover restart still fires.
func TestInstallCheckSelfRefreshesDriftedUnit(t *testing.T) {
	old := "[Unit]\nDescription=DevLab backend (devlabd)\n[Service]\nUser=devlab\nExecStart=/usr/local/bin/devlabd\n"
	env, artifact := selfUnitFixture(t, selfDeliveredUnit, old, "")
	// The installed unit is OURS: systemd's fragment path is the one in our unit dir → an update.
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, filepath.Join(env["DEVLAB_UNIT_DIR"], "devlabd.service"))

	res := runWrapper(t, "deploy/devlab-install", env, "devlab", artifact, "dev", "--check", "--handover")
	if res.exit != 0 {
		t.Fatalf("a self refresh must exit 0, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "PLAN: install") || !strings.Contains(res.out, "devlabd.service") {
		t.Errorf("a drifted unit must be refreshed from the delivered copy: %s", res.out)
	}
	if !strings.Contains(res.out, "daemon-reload") {
		t.Errorf("refreshing the unit must reload systemd so the new directive takes effect: %s", res.out)
	}
	if !strings.Contains(res.out, "systemd-run") || !strings.Contains(res.out, "devlab-restart-when-free") {
		t.Errorf("the handover restart must still fire (the group applies on restart): %s", res.out)
	}
}

// The installed unit already matches the delivered one: no refresh, no reload — only the handover.
func TestInstallCheckSelfUnchangedUnitNotRefreshed(t *testing.T) {
	env, artifact := selfUnitFixture(t, selfDeliveredUnit, selfDeliveredUnit, "")
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, filepath.Join(env["DEVLAB_UNIT_DIR"], "devlabd.service"))

	res := runWrapper(t, "deploy/devlab-install", env, "devlab", artifact, "dev", "--check", "--handover")
	if res.exit != 0 {
		t.Fatalf("an up-to-date self unit must exit 0, got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "daemon-reload") {
		t.Errorf("an unchanged unit must not be reinstalled or reloaded: %s", res.out)
	}
	if !strings.Contains(res.out, "already matches the delivered unit") {
		t.Errorf("the no-op must be named, not silent: %s", res.out)
	}
}

// A FOREIGN devlabd.service (a fragment this wrapper did not install) is refused, never overwritten.
func TestInstallCheckSelfRefusesForeignUnit(t *testing.T) {
	env, artifact := selfUnitFixture(t, selfDeliveredUnit, "", "/lib/systemd/system/devlabd.service")

	res := runWrapper(t, "deploy/devlab-install", env, "devlab", artifact, "dev", "--check", "--handover")
	if res.exit != 5 {
		t.Fatalf("a foreign devlabd unit must be refused with exit 5, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "foreign unit") {
		t.Errorf("the refusal must name the foreign unit: %s", res.out)
	}
}

// A self artifact that ships no setup/ unit (an older build) leaves the installed unit unchanged — the
// binary install still happens and the handover still fires.
func TestInstallCheckSelfNoDeliveredUnitLeavesUnchanged(t *testing.T) {
	env, artifact := selfFixture(t, "devlab", testOwner)
	env["DEVLAB_UNIT_DIR"] = t.TempDir()
	env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")

	res := runWrapper(t, "deploy/devlab-install", env, "devlab", artifact, "dev", "--check", "--handover")
	if res.exit != 0 {
		t.Fatalf("a self install without a delivered unit must exit 0, got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "daemon-reload") {
		t.Errorf("without a delivered unit nothing is refreshed: %s", res.out)
	}
	if !strings.Contains(res.out, "installed unit left unchanged") {
		t.Errorf("the skip must be named: %s", res.out)
	}
}
