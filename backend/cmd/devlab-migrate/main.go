// devlab-migrate — the ONE-TIME data import (S15). Its own binary; it is never installed as a
// service and has no HTTP endpoint. It REFUSES to run while the daemon is alive (a probe on
// the ready socket, B-9), and it is idempotent: a second run changes nothing.
//
// Input: the raw legacy run set (--input mercury-runs-roh.json — instance data, never in the
// repository). It imports the foreign todos (1 open, 6 as completed history entries with their
// original metadata), creates the 7 automatic runs WITHOUT axiom assignment (uncovered stays
// visible; the auto-assignment plans on the first constitution write, B-10), imports the
// legacy execution history tolerantly into executions/ (legacy states stay viewable, are
// never produced anew), and prepares the M1–M8 migration notices with their outcome column.
//
// Exit codes follow the uniform Holistic CLI convention (the `service` verbs use the same set):
// 0 ok · 1 generic · 2 usage · 4 external · 5 config-state · 10 declined.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"devlab/backend/internal/statepath"
)

// Exit codes of the uniform CLI convention.
const (
	exitOK          = 0
	exitGeneric     = 1
	exitUsage       = 2
	exitConfigState = 5
	exitDeclined    = 10
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole tool, testable: it returns the process exit code and writes the protocol to
// out, every refusal to errOut.
func run(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("devlab-migrate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	input := fs.String("input", "", "path to the raw legacy run export (mercury-runs-roh.json)")
	dryRun := fs.Bool("dry-run", false, "report what the import would do and write nothing")
	fs.Usage = func() {
		fmt.Fprintln(errOut, "usage: devlab-migrate --input <run-export.json> [--dry-run]")
		fmt.Fprintln(errOut, "\nThe state root comes from DEVLAB_STATE_DIR (or systemd's StateDirectory).")
		fmt.Fprintln(errOut, "The order is stop → migrate → start: the import refuses while the ready socket answers.")
		fmt.Fprintln(errOut, "\nExit codes: 0 ok · 1 generic · 2 usage · 5 config-state · 10 declined")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errOut, "devlab-migrate: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	if *input == "" {
		fmt.Fprintln(errOut, "devlab-migrate: --input is required")
		fs.Usage()
		return exitUsage
	}

	paths, err := statepath.FromEnv()
	if err != nil {
		fmt.Fprintf(errOut, "devlab-migrate: %v\n", err)
		return exitConfigState
	}
	// B-9: the daemon owns the pools while it lives. An answering ready socket — free (204) or
	// busy (423) — means it is alive, and the import declines. A dead daemon reads as free: a
	// stale socket file cannot be dialled, so a crash does not block the cutover.
	if code, alive := daemonAlive(paths.ReadySocket()); alive {
		fmt.Fprintf(errOut, "devlab-migrate: declined — the service is running (ready socket answered %d). "+
			"Stop it first: the import order is stop → migrate → start.\n", code)
		return exitDeclined
	}

	m := newMigrator(paths, ownRepo, time.Now())
	p, err := m.plan(*input)
	if err != nil {
		fmt.Fprintf(errOut, "devlab-migrate: %v\n", err)
		return exitConfigState
	}
	writeProtocol(out, p, *dryRun)

	if len(p.refusals) > 0 {
		fmt.Fprintln(errOut, "devlab-migrate: nothing was imported — the export holds records that cannot be mapped faithfully (listed above)")
		return exitConfigState
	}
	if *dryRun || p.empty() {
		return exitOK
	}
	if err := m.apply(p); err != nil {
		fmt.Fprintf(errOut, "devlab-migrate: %v\n", err)
		if errors.Is(err, errRefused) {
			return exitConfigState
		}
		return exitGeneric
	}
	fmt.Fprintln(out, "imported.")
	return exitOK
}

// daemonAlive probes the ready socket. It returns the status code it got and whether anything
// answered at all.
func daemonAlive(sock string) (int, bool) {
	c := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
	// The host part is ignored by a unix-socket dialer; the path is the readiness endpoint.
	resp, err := c.Get("http://devlab-ready/ready")
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, true
}
