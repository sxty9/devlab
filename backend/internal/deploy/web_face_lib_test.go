package deploy

// THE DELIVERED FACE — a package that carries a web bundle gets it INSTALLED, at the serve root the
// PACKAGE declares.
//
// What was measured on 2026-08-09: the holistic dashboard's built bundle lay complete in production's
// staging directory (index.html + assets) while /opt/holistic/www did not exist. The face travelled and
// was dropped on arrival, and the delivery reported success — because the install of a web bundle
// existed for exactly ONE repository (the self repo), in a branch of its own, and every other service
// fell past it. These tests exercise the shared function that replaced that branch
// (setup_install_web), FOR REAL, against real directories: what it installs, and the three incomplete
// packages it refuses BY NAME rather than pass over.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// facePackage stages an artifact the way a build produces one: a built web bundle plus the `web.root`
// declaration that says where it belongs on the target host. declare="" stages the bundle WITHOUT the
// declaration (the package that says nothing about its own face).
func facePackage(t *testing.T, declare string, withBundle bool) string {
	t.Helper()
	art := t.TempDir()
	if withBundle {
		web := filepath.Join(art, "web")
		if err := os.MkdirAll(filepath.Join(web, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Written with the build account's group-only mode — the shape that made the edge answer 404 over
		// a present page. The install must deliver it readable BY ITS ROLE regardless.
		if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>dashboard</html>"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(web, "index.html"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if declare != "" {
		if err := os.WriteFile(filepath.Join(art, "web.root"), []byte(declare+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return art
}

// DECISIVE: a delivered face IS installed — at the serve root the package declares, not at one derived
// from the repository name — and it arrives readable by the webserver's role. This is the half whose
// absence left the landscape dashboard's bundle in the staging directory.
func TestLibInstallWebInstallsWhereThePackageDeclares(t *testing.T) {
	host := t.TempDir() // this fixture host's service tree
	root := filepath.Join(host, "holistic", "www")
	art := facePackage(t, root, true)

	res := sourceLib(t, map[string]string{"DEVLAB_SERVICE_ROOT": host},
		"setup_install_web "+art+" holistic")
	if res.exit != 0 {
		t.Fatalf("a declared face must install (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "MERCURY-WEB: installed | "+root) {
		t.Errorf("the outcome must be stated on the one machine-readable line naming the serve root:\n%s", res.out)
	}
	index := filepath.Join(root, "index.html")
	fi, err := os.Stat(index)
	if err != nil {
		t.Fatalf("the face must be ON the host after the install: %v", err)
	}
	// Readable by ITS PUBLIC ROLE, not by whoever built it: the browser fetches every byte through the
	// edge, which runs as a different account entirely.
	if fi.Mode().Perm()&0o004 == 0 {
		t.Errorf("the delivered start page must be readable by the webserver's role, got mode %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, "assets")); err != nil {
		t.Errorf("the whole bundle must arrive, not only its start page: %v", err)
	}
}

// A package that carries a face but says nothing about where it belongs is an INCOMPLETE PACKAGE, and
// it is refused by name. The installer does not fall back to a destination derived from the repository
// name — that derivation is the fault this whole change removes.
func TestLibInstallWebRefusesAFaceWithoutADeclaration(t *testing.T) {
	art := facePackage(t, "", true)
	res := sourceLib(t, nil, "setup_install_web "+art+" holistic")
	if res.exit == 0 {
		t.Fatalf("a face without a declared serve root must be refused, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "MERCURY-WEB: failed") {
		t.Errorf("the refusal must be stated on the machine-readable line too:\n%s", res.out)
	}
	for _, want := range []string{"declares no serve root", "never derived from the repository name"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the refusal must name %q:\n%s", want, res.out)
		}
	}
}

// The mirror case: a package that ANNOUNCES a face which did not travel. That is an incomplete
// delivery, not a service without a face, and the difference is stated rather than guessed.
func TestLibInstallWebRefusesADeclarationWithoutAFace(t *testing.T) {
	art := facePackage(t, "/opt/holistic/www", false)
	res := sourceLib(t, nil, "setup_install_web "+art+" holistic")
	if res.exit == 0 {
		t.Fatalf("a declared face that did not travel must be refused, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "carries no web/") || !strings.Contains(res.out, "incomplete delivery") {
		t.Errorf("the refusal must name the announced-but-absent face:\n%s", res.out)
	}
}

// A package may place its face inside ITSELF and nowhere else. The declaration says WHERE; it does not
// grant where — otherwise a repository could aim a root-run `rsync --delete` at any directory on the
// host. Every escape is refused by name, and nothing is written.
func TestLibInstallWebRefusesADeclarationOutsideTheServiceTerritory(t *testing.T) {
	for _, decl := range []string{
		"/etc/caddy",                 // a system directory
		"/opt/prizm/www",             // another service's tree
		"/var/lib/prizm/www",         // another service's state tree
		"/opt/holistic/../prizm/www", // a traversal dressed as our own tree
		"relative/www",               // not absolute
		"/opt/holistic",              // the service tree itself, not a serve root inside it
	} {
		art := facePackage(t, decl, true)
		res := sourceLib(t, nil, "setup_install_web "+art+" holistic")
		if res.exit == 0 {
			t.Errorf("the declaration %q lies outside the service's territory and must be refused:\n%s", decl, res.out)
		}
		if !strings.Contains(res.out, "MERCURY-WEB: failed") {
			t.Errorf("the declaration %q must be refused on the machine-readable line too:\n%s", decl, res.out)
		}
	}
	// …and the refusal happens BEFORE any effect: an inadmissible destination is not created, so nothing
	// of the package reaches a place it may not go.
	outside := filepath.Join(t.TempDir(), "elsewhere", "www")
	art := facePackage(t, outside, true)
	if res := sourceLib(t, nil, "setup_install_web "+art+" holistic"); res.exit == 0 {
		t.Fatalf("a destination outside the service's territory must be refused:\n%s", res.out)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("a refused declaration must leave nothing behind, but %s was created", outside)
	}
}

// A service without a face says so — the half is stated, never silently omitted, so a reader can tell
// "ships no interface" apart from "its interface never arrived".
func TestLibInstallWebStatesTheAbsentHalf(t *testing.T) {
	art := facePackage(t, "", false)
	res := sourceLib(t, nil, "setup_install_web "+art+" prizm")
	if res.exit != 0 {
		t.Fatalf("a package without a face is not a fault, got exit %d\n%s", res.exit, res.out)
	}
	if strings.TrimSpace(res.out) != "MERCURY-WEB: none" {
		t.Errorf("a service without a face must say so on the one line:\n%s", res.out)
	}
}

// The dry run judges everything and copies nothing, so a plan can never read clean where the real run
// would fail — and a plan that WOULD fail says so with the same reason.
func TestLibInstallWebPlanOnlyJudgesWithoutCopying(t *testing.T) {
	host := t.TempDir()
	root := filepath.Join(host, "holistic", "www")
	art := facePackage(t, root, true)

	res := sourceLib(t, map[string]string{"DEVLAB_SERVICE_ROOT": host},
		"setup_install_web "+art+" holistic 1")
	if res.exit != 0 {
		t.Fatalf("the plan must judge cleanly, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "MERCURY-WEB: planned | "+root) {
		t.Errorf("the plan must state the planned half and its serve root:\n%s", res.out)
	}
	if _, err := os.Stat(root); err == nil {
		t.Errorf("a dry run must copy nothing, but %s exists", root)
	}

	// A package the real run would refuse is refused in the plan too, at the same place.
	bad := facePackage(t, "/etc/caddy", true)
	if res := sourceLib(t, nil, "setup_install_web "+bad+" holistic 1"); res.exit == 0 {
		t.Errorf("a plan over an inadmissible declaration must fail like the real run:\n%s", res.out)
	}
}

// WHICH application the instance root belongs to is decided ONCE, in the shared library, as a landscape
// fact: the root application is the landscape's dashboard. Its serve root follows the same serve
// convention every service's face does (/opt/<repo>/www, symmetric to /opt/<repo>/bin), so the edge can
// name it before any delivery has arrived on the host.
func TestLibRootApplicationIsDecidedOnceInTheLibrary(t *testing.T) {
	res := sourceLib(t, nil, `printf '%s %s %s\n' "$SETUP_ROOT_APP" "$(setup_root_app_www)" "$(setup_service_www prizm)"`)
	if res.exit != 0 {
		t.Fatalf("the root-application decision must be readable from the library: %d\n%s", res.exit, res.out)
	}
	if got := strings.TrimSpace(res.out); got != "holistic /opt/holistic/www /opt/prizm/www" {
		t.Errorf("the landscape's root application and the serve convention must hold, got %q", got)
	}
}
