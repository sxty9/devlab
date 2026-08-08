package deploy

// The gate proves the port the DELIVERED unit BINDS, read from the same setup product that names the
// unit — not the port the chain's allocation computed. Measured 2026-08-08 on holistic-dashboard: the
// delivered unit bound 8770, the allocation proposed 8785 (it saw the service's own bound-unrouted 8770
// as taken), and the gate dialed 8785 and booked a LIVE service as failed. These seams pin the fix.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupArtifactWithUnit builds an artifact dir carrying setup/<unit>.service with the given ExecStart.
func setupArtifactWithUnit(t *testing.T, unit, execStart string) string {
	t.Helper()
	art := t.TempDir()
	setup := filepath.Join(art, "setup")
	if err := os.MkdirAll(setup, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[Unit]\nDescription=x\n[Service]\nUser=holistic\n" + execStart + "\n"
	if err := os.WriteFile(filepath.Join(setup, unit+".service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return art
}

// DeliveredUnitPort reads the port the unit's ExecStart binds, in the two forms that occur across the
// build kinds: the go-daemon template's `--listen 127.0.0.1:<port>` (a colon) and a python-app's uvicorn
// `--port <port>` / `--port=<port>`. It is the Go twin of the shell setup_unit_listen_port.
func TestDeliveredUnitPort(t *testing.T) {
	cases := []struct {
		name string
		exec string
		want int
	}{
		{"go-daemon loopback bind", "ExecStart=/opt/x/bin/xd --listen 127.0.0.1:8770", 8770},
		{"python --port with a space", "ExecStart=/opt/x/venv/bin/uvicorn app:app --host 127.0.0.1 --port 8770", 8770},
		{"python --port=equals", "ExecStart=/opt/x/venv/bin/uvicorn app:app --port=8791", 8791},
		{"loopback bind wins over a stray flag", "ExecStart=/opt/x/xd --listen 127.0.0.1:8770 --metrics-port 9000", 8770},
		{"no port at all", "ExecStart=/opt/x/bin/xd --serve", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			art := setupArtifactWithUnit(t, "holistic-dashboard", c.exec)
			if got := DeliveredUnitPort(art, "holistic-dashboard"); got != c.want {
				t.Errorf("DeliveredUnitPort = %d, want %d", got, c.want)
			}
		})
	}
	if got := DeliveredUnitPort(t.TempDir(), "holistic-dashboard"); got != 0 {
		t.Errorf("a package that ships no such unit must yield 0, got %d", got)
	}
	if got := DeliveredUnitPort(setupArtifactWithUnit(t, "holistic-dashboard", "ExecStart=x"), ""); got != 0 {
		t.Errorf("an empty unit name must yield 0, got %d", got)
	}
}

// capturingPorts captures the desired port fed to the allocation, so a test can prove the DELIVERED
// unit's port drives the conflict check (task point 3) — not the repo declaration or a bare 0.
type capturingPorts struct {
	gotDesired int
	port       int
	err        error
}

func (r *capturingPorts) PortFor(_ context.Context, _ string, desired int) (int, bool, error) {
	r.gotDesired = desired
	return r.port, false, r.err
}

// TEST a (the decisive one): a service whose delivered unit names a port is delivered → the gate proves
// EXACTLY that port, and reports it, even though the allocation proposed a different one.
func TestDeliverDevProvesDeliveredUnitPortNotAllocated(t *testing.T) {
	art := setupArtifactWithUnit(t, "holistic-dashboard",
		"ExecStart=/opt/holistic/venv/bin/uvicorn app:app --host 127.0.0.1 --port 8770")
	ports := &capturingPorts{port: 8785} // the WRONG, allocated port the old gate dialed
	ri := &fakeInstaller{}
	var probed int
	gate := Gate{
		UnitActive: func(context.Context, string) error { return nil },
		PortHeld:   func(_ context.Context, p int) error { probed = p; return nil },
		Wait:       10 * time.Millisecond, Poll: time.Millisecond,
	}
	out, err := DeliverDev(context.Background(), ri, ports, gate,
		Detection{Kind: KindService, ID: "holistic", Evidence: "./service"}, "holistic", art)
	if err != nil {
		t.Fatalf("a live delivered service must pass its gate: %v", err)
	}
	if probed != 8770 {
		t.Errorf("the gate must dial the DELIVERED unit's port 8770, dialed %d", probed)
	}
	if out.Port != 8770 {
		t.Errorf("the outcome must report the delivered port 8770, got %d", out.Port)
	}
	if ports.gotDesired != 8770 {
		t.Errorf("the delivered port must drive the allocation's conflict check as desired=8770, got %d", ports.gotDesired)
	}
	if len(ri.calls) != 1 || !strings.Contains(ri.calls[0], "port=8770") {
		t.Errorf("the install must carry the delivered port, not the allocated one: %v", ri.calls)
	}
}

// TEST b: a service with NO fixed port (no setup unit) still gets a free one from the allocation, exactly
// as before — the allocation keeps its one purpose (task point 2). The delivered port never invents one.
func TestDeliverDevNoDeliveredPortFallsBackToAllocation(t *testing.T) {
	ports := &capturingPorts{port: 8772}
	ri := &fakeInstaller{}
	var probed int
	gate := Gate{
		UnitActive: func(context.Context, string) error { return nil },
		PortHeld:   func(_ context.Context, p int) error { probed = p; return nil },
		Wait:       10 * time.Millisecond, Poll: time.Millisecond,
	}
	out, err := DeliverDev(context.Background(), ri, ports, gate,
		Detection{Kind: KindService, ID: "svc", Evidence: "./service", Decl: &DeclarationFile{Port: 8781}}, "svc", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ports.gotDesired != 8781 {
		t.Errorf("absent a delivered unit the declared port is desired (8781), got %d", ports.gotDesired)
	}
	if probed != 8772 || out.Port != 8772 {
		t.Errorf("the allocated port must stand when the unit fixes none: probed=%d port=%d", probed, out.Port)
	}
}

// TEST c: a FOREIGN service holds the delivered unit's port → the allocation rejects it as a conflict,
// asked about the DELIVERED port, and nothing is installed (task point 3, "wie bisher").
func TestDeliverDevDeliveredPortForeignConflictRejected(t *testing.T) {
	art := setupArtifactWithUnit(t, "holistic-dashboard", "ExecStart=/opt/x/xd --listen 127.0.0.1:8770")
	occ := errors.New("port 8770 is held by other; 8790 is free")
	ports := &capturingPorts{err: occ}
	ri := &fakeInstaller{}
	_, err := DeliverDev(context.Background(), ri, ports, greenGate(),
		Detection{Kind: KindService, ID: "holistic", Evidence: "./service"}, "holistic", art)
	if !errors.Is(err, occ) {
		t.Fatalf("a foreign holder of the delivered port must reject the delivery: %v", err)
	}
	if ports.gotDesired != 8770 {
		t.Errorf("the conflict check must be asked about the DELIVERED port 8770, got %d", ports.gotDesired)
	}
	if len(ri.calls) != 0 {
		t.Errorf("a rejected port must never reach the installer: %v", ri.calls)
	}
}
