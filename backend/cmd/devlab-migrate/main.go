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
// B13 fills the implementation; this stub pins the contract: flags, refusal, exit codes.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	input := flag.String("input", "", "path to the raw legacy run export (mercury-runs-roh.json)")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "devlab-migrate: --input is required")
		os.Exit(2)
	}
	// TODO(B13): statepath.FromEnv → refuse when the ready socket answers (daemon alive, B-9)
	// → idempotent import → notice protocol.
	fmt.Fprintln(os.Stderr, "devlab-migrate: not implemented yet (S15, Welle 2)")
	os.Exit(1)
}
