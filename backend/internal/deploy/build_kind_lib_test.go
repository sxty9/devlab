package deploy

// The build-kind vocabulary and the venv relocation live in the ONE shared library devlab-install,
// devlab-deploy-recv and devlab-exec all source, so none of them can drift on what a valid Bauart is or
// on how a prebuilt virtualenv is made runnable from its final install path. These tests exercise the
// library functions directly (by sourcing it in bash), so the shared contract is pinned in one place.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibValidBuildKind(t *testing.T) {
	for _, k := range []string{"go-daemon", "python-app"} {
		if res := sourceLib(t, nil, "setup_valid_build_kind "+k); res.exit != 0 {
			t.Errorf("%q must be a valid build kind", k)
		}
	}
	for _, k := range []string{"", "go", "python", "ruby-gem", "GO-DAEMON"} {
		if res := sourceLib(t, nil, "setup_valid_build_kind '"+k+"'"); res.exit == 0 {
			t.Errorf("%q must NOT be a valid build kind", k)
		}
	}
}

func TestLibReadBuildKind(t *testing.T) {
	art := t.TempDir()
	// Absent build.kind → empty (the caller refuses by name; the reader does not guess).
	if res := sourceLib(t, nil, "setup_read_build_kind "+art); strings.TrimSpace(res.out) != "" {
		t.Errorf("an artifact with no build.kind must read as empty, got %q", res.out)
	}
	if err := os.WriteFile(filepath.Join(art, "build.kind"), []byte(" python-app \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := sourceLib(t, nil, "setup_read_build_kind "+art); strings.TrimSpace(res.out) != "python-app" {
		t.Errorf("the trimmed first line must be read, got %q", res.out)
	}
}

// setup_relocate_venv makes a --copies venv built at one path runnable from another without a rebuild: it
// rewrites the shebang of every bin/ script that names the BUILD path to the FINAL install path, and
// leaves everything else — the interpreter binary, the script bodies — byte-for-byte. This is the seam
// that lets prod stay a pure copy for a python-app (no build on target).
func TestLibRelocateVenv(t *testing.T) {
	venv := filepath.Join(t.TempDir(), "venv")
	bin := filepath.Join(venv, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A console script whose shebang names the build path, with a body to preserve.
	script := "#!" + venv + "/bin/python\nimport sys\nprint('hi')\n"
	if err := os.WriteFile(filepath.Join(bin, "uvicorn"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A file whose shebang does NOT name the build path — must be left untouched.
	other := "#!/usr/bin/env python3\nx = 1\n"
	if err := os.WriteFile(filepath.Join(bin, "unrelated"), []byte(other), 0o755); err != nil {
		t.Fatal(err)
	}

	final := "/opt/holistic/venv"
	if res := sourceLib(t, nil, "setup_relocate_venv "+venv+" "+final); res.exit != 0 {
		t.Fatalf("relocate must succeed, got exit %d\n%s", res.exit, res.out)
	}

	got, err := os.ReadFile(filepath.Join(bin, "uvicorn"))
	if err != nil {
		t.Fatal(err)
	}
	want := "#!" + final + "/bin/python\nimport sys\nprint('hi')\n"
	if string(got) != want {
		t.Errorf("the shebang must be rewritten to the final path and the body preserved:\n got %q\nwant %q", got, want)
	}
	// The relocated script must stay executable.
	if fi, _ := os.Stat(filepath.Join(bin, "uvicorn")); fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("the relocated console script must remain executable, got mode %v", fi.Mode())
	}
	// The unrelated shebang is left exactly as it was.
	if u, _ := os.ReadFile(filepath.Join(bin, "unrelated")); string(u) != other {
		t.Errorf("a shebang not naming the build path must be untouched, got %q", u)
	}
}

// buildRealBundle creates a real --copies venv and runs setup_bundle_python on it, returning the payload
// directory (holding venv/ + pybase/) and the bundled X.Y version. It uses the host's real python3, so it
// exercises the actual copy-and-repoint the delivery chain performs.
func buildRealBundle(t *testing.T) (payload, minor string) {
	t.Helper()
	needTool(t, "python3")
	payload = t.TempDir()
	venv := filepath.Join(payload, "venv")
	if out, err := exec.Command("python3", "-m", "venv", "--copies", venv).CombinedOutput(); err != nil {
		t.Fatalf("could not create a --copies venv: %v\n%s", err, out)
	}
	res := sourceLib(t, nil, "setup_bundle_python "+venv+" /opt/holistic/venv")
	if res.exit != 0 {
		t.Fatalf("setup_bundle_python must succeed, got exit %d\n%s", res.exit, res.out)
	}
	minor = strings.TrimSpace(res.out)
	if minor == "" {
		t.Fatalf("setup_bundle_python must echo the bundled X.Y version, got %q", res.out)
	}
	return payload, minor
}

// TASK TEST a (the decisive one), at the seam that makes it true: after setup_bundle_python, the venv
// resolves its standard library from the BUNDLED interpreter shipped in the payload — NOT from the host's
// /usr — so a target whose python differs from the build host's still runs the service. We prove it by
// MOVING the payload to a new prefix (the build path no longer exists, exactly as on a target) and asking
// the bundled interpreter where `encodings` comes from: it must be inside the moved payload.
func TestLibBundlePythonSelfContained(t *testing.T) {
	payload, minor := buildRealBundle(t)

	// The bundle carries the interpreter and its standard library.
	base := filepath.Join(payload, "pybase", "bin", "python"+minor)
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("the bundle must carry the interpreter at pybase/bin/python%s: %v", minor, err)
	}
	if _, err := os.Stat(filepath.Join(payload, "pybase", "lib", "python"+minor, "encodings")); err != nil {
		t.Fatalf("the bundle must carry the standard library (encodings) under pybase/lib/python%s: %v", minor, err)
	}
	// The venv is re-pointed at the bundled base at its FINAL location, not the build host's /usr.
	cfg, err := os.ReadFile(filepath.Join(payload, "venv", "pyvenv.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "home = /opt/holistic/pybase/bin") {
		t.Errorf("pyvenv.cfg home must point at the bundled base's FINAL path, got:\n%s", cfg)
	}

	// Simulate the target: move the whole payload to a path the build never used, then run the bundled
	// interpreter standalone. It resolves its stdlib relative to its own location, so encodings must load
	// from INSIDE the moved payload — proving the payload owes nothing to the host's own python.
	moved := filepath.Join(t.TempDir(), "opt-holistic")
	if err := os.Rename(payload, moved); err != nil {
		t.Fatal(err)
	}
	movedBase := filepath.Join(moved, "pybase", "bin", "python"+minor)
	out, err := exec.Command("env", "-i", movedBase, "-c",
		"import encodings,sys; sys.stdout.write(encodings.__file__)").CombinedOutput()
	if err != nil {
		t.Fatalf("the bundled interpreter must run after the payload is moved (target simulation): %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), moved+string(os.PathSeparator)) {
		t.Errorf("the standard library must resolve from the MOVED bundle, not the host's /usr — got %q", out)
	}
}

// The pre-copy self-test PASSES for a well-formed bundle (setup_python_payload_selftest echoes the version),
// so the receiver proceeds to copy only a payload proven runnable on the host.
func TestLibPythonSelftestPasses(t *testing.T) {
	payload, minor := buildRealBundle(t)
	res := sourceLib(t, nil, "setup_python_payload_selftest "+payload)
	if res.exit != 0 {
		t.Fatalf("the self-test must pass for a real bundle, got exit %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, minor) {
		t.Errorf("the self-test must echo the proven version %s, got %q", minor, res.out)
	}
}

// TASK TEST c (unit level): the self-test NAMES an incompatible or absent bundle and returns non-zero — the
// pre-copy refusal, before any copy. A payload without a bundled interpreter, and one whose interpreter
// fails to run (an older C library than the build host), are both refused with a named reason.
func TestLibPythonSelftestRefusesNamed(t *testing.T) {
	// Absent bundle: an old-style, non-relocatable payload.
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := sourceLib(t, nil, "setup_python_payload_selftest "+empty)
	if res.exit == 0 {
		t.Fatalf("a payload with no bundled interpreter must be refused, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "no bundled interpreter") {
		t.Errorf("the refusal must name the missing bundle: %s", res.out)
	}

	// Present but unrunnable interpreter (simulated glibc mismatch): a fake python that exits non-zero.
	bad := t.TempDir()
	bin := filepath.Join(bad, "pybase", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'libc.so.6: version GLIBC_2.41 not found' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "python3.14"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res = sourceLib(t, nil, "setup_python_payload_selftest "+bad)
	if res.exit == 0 {
		t.Fatalf("an unrunnable bundled interpreter must be refused, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "cannot run on this host") {
		t.Errorf("the refusal must name that the interpreter cannot run here: %s", res.out)
	}
}
