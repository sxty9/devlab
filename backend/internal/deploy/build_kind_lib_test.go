package deploy

// The build-kind vocabulary and the venv relocation live in the ONE shared library devlab-install,
// devlab-deploy-recv and devlab-exec all source, so none of them can drift on what a valid Bauart is or
// on how a prebuilt virtualenv is made runnable from its final install path. These tests exercise the
// library functions directly (by sourcing it in bash), so the shared contract is pinned in one place.

import (
	"os"
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
