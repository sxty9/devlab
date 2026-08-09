package deploy

// The production-receiver drift check MEASURES what the production host carries against the merged
// delivery, from the receiver's own self-report. These tests prove: the self-report is parsed
// faithfully; a host that carries exactly the merged scripts shows NO drift; a host carrying a
// different version, OR one that reports nothing at all (an older receiver that predates
// self-reporting), shows drift — so an unproven receiver is never mistaken for a current one.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRecv(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// seedRecvRepo commits a deploy/ tree carrying the two receiver scripts with the given bytes and
// returns the working tree and the sha256 of each committed script (the checksum a verbatim install
// would have). The commit is on a local branch named like the merged stand the check reads.
func seedRecvRepo(t *testing.T, recvBytes, libBytes []byte) (wt, ref string, recvSHA, libSHA string) {
	t.Helper()
	wt = t.TempDir()
	gitRecv(t, wt, "init", "-q", "-b", "main")
	deployDir := filepath.Join(wt, "deploy")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "devlab-deploy-recv"), recvBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "devlab-setup-lib.sh"), libBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRecv(t, wt, "add", "-A")
	gitRecv(t, wt, "commit", "-qm", "merged receiver")
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	return wt, "main", sum(recvBytes), sum(libBytes)
}

func TestParseReceiverSelfSHA(t *testing.T) {
	out := "install-only prod deploy of 'devlab'\n" +
		"RECV-SELF-SHA devlab-deploy-recv aaa111\n" +
		"noise\n" +
		"  RECV-SELF-SHA devlab-setup-lib.sh bbb222  \n" +
		"prod deploy of 'devlab' done\n"
	got := ParseReceiverSelfSHA(out)
	if got["devlab-deploy-recv"] != "aaa111" || got["devlab-setup-lib.sh"] != "bbb222" {
		t.Fatalf("parsed self-report wrong: %v", got)
	}
	if len(ParseReceiverSelfSHA("no report here")) != 0 {
		t.Fatal("output without RECV-SELF-SHA lines must parse to an empty map")
	}
}

// A host that reports EXACTLY the merged checksums shows no drift — the receiver is current, so the
// delivery may settle live.
func TestReceiverDrift_HostCurrent_NoDrift(t *testing.T) {
	wt, ref, recvSHA, libSHA := seedRecvRepo(t, []byte("recv v2\n"), []byte("lib v2\n"))
	reported := map[string]string{"devlab-deploy-recv": recvSHA, "devlab-setup-lib.sh": libSHA}
	drifts, stale := ReceiverDrift(wt, ref, reported)
	if stale || len(drifts) != 0 {
		t.Fatalf("a host carrying exactly the merged scripts must show no drift, got %v", drifts)
	}
}

// A host that carries a DIFFERENT version of a receiver script shows drift, and the drift names the
// checksum the merged delivery needs.
func TestReceiverDrift_HostStale_Drifts(t *testing.T) {
	wt, ref, recvSHA, libSHA := seedRecvRepo(t, []byte("recv v2\n"), []byte("lib v2\n"))
	// The host carries the OLD receiver but the current library.
	reported := map[string]string{"devlab-deploy-recv": "oldsha000", "devlab-setup-lib.sh": libSHA}
	drifts, stale := ReceiverDrift(wt, ref, reported)
	if !stale || len(drifts) != 1 {
		t.Fatalf("a host carrying an older receiver must drift on exactly that script, got %v", drifts)
	}
	if drifts[0].Name != "devlab-deploy-recv" || drifts[0].WantSHA != recvSHA {
		t.Fatalf("the drift must name devlab-deploy-recv and the merged checksum %s, got %+v", recvSHA, drifts[0])
	}
}

// A host that reports NOTHING (an older receiver that predates self-reporting) is NOT proven current,
// so every receiver script drifts — absence of proof is not proof.
func TestReceiverDrift_SilentReceiver_AllDrift(t *testing.T) {
	wt, ref, _, _ := seedRecvRepo(t, []byte("recv v2\n"), []byte("lib v2\n"))
	drifts, stale := ReceiverDrift(wt, ref, map[string]string{})
	if !stale || len(drifts) != len(ReceiverScripts) {
		t.Fatalf("a silent (older) receiver must be treated as stale on every script, got %v", drifts)
	}
}
