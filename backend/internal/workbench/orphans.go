// Orphan rescue (REQ-023.5): committed work that no ref reaches anymore is secured under
// refs/devlab/rescue/* and reported — the workbench machinery would rather keep one ref too
// many than lose one commit.
package workbench

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Orphan is a commit that would otherwise be lost, secured under a rescue ref.
type Orphan struct {
	Commit    string
	RescueRef string // refs/devlab/rescue/<ts>-<short>
	Pushed    bool
}

// rescueRefPrefix is the ref namespace sheltering rescued commits — outside refs/heads, so
// rescues never clutter branch lists and never look like deliverable work.
const rescueRefPrefix = "refs/devlab/rescue/"

// maxOrphanSweep caps one sweep; an avalanche beyond it is logged and finished next time.
const maxOrphanSweep = 64

// CollectOrphans secures orphaned commits: a rescue ref refs/devlab/rescue/<ts>-<short>, a
// best-effort push, and a notice (REQ-023.5). Orphans are commits reachable from NO ref at
// all (branches, remotes and existing rescues all count as reachable; reflogs deliberately
// do NOT — they expire). Only chain tips get a ref: securing a tip secures its whole
// history. Once rescued, a commit is reachable and never reported again — the sweep is
// idempotent.
func (b *Bench) CollectOrphans(ctx context.Context) ([]Orphan, error) {
	out, err := b.gitRO(ctx, "fsck", "--connectivity-only", "--no-reflogs", "--unreachable", "--no-progress")
	if err != nil {
		return nil, fmt.Errorf("orphan sweep: %w", err)
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "unreachable" && fields[1] == "commit" {
			shas = append(shas, fields[2])
		}
	}
	if len(shas) == 0 {
		return nil, nil
	}
	sort.Strings(shas) // deterministic sweep order
	if len(shas) > maxOrphanSweep {
		log.Printf("workbench: %s — %d orphaned commits found, securing the first %d this sweep", b.repo, len(shas), maxOrphanSweep)
		shas = shas[:maxOrphanSweep]
	}
	tips := chainTips(ctx, b, shas)

	var orphans []Orphan
	for _, sha := range tips {
		short := sha
		if len(short) > 8 {
			short = short[:8]
		}
		ref := rescueRefPrefix + time.Now().UTC().Format("20060102-150405") + "-" + short
		if _, err := b.gitUser(ctx, "update-ref", ref, sha); err != nil {
			return orphans, fmt.Errorf("rescue %s: %w", short, err)
		}
		o := Orphan{Commit: sha, RescueRef: ref}
		// Best-effort backup of the rescue — a failed push keeps the local ref and is retried
		// implicitly by a later sweep... of a later orphan; the LOCAL rescue already prevents
		// the loss (gc respects refs).
		if _, err := b.ex.PushRefs(ctx, b.repo, b.token, false, ref); err == nil {
			o.Pushed = true
		}
		log.Printf("workbench: %s — orphaned commit %s secured as %s (pushed=%v)", b.repo, short, ref, o.Pushed)
		orphans = append(orphans, o)
	}
	return orphans, nil
}

// chainTips drops every sha that is an ancestor of another candidate, so one rescue ref
// shelters one whole orphaned chain. Read-only; a failing ancestry probe keeps the commit
// (rescuing one ref too many beats losing one commit).
func chainTips(ctx context.Context, b *Bench, shas []string) []string {
	if len(shas) == 1 {
		return shas
	}
	tips := make([]string, 0, len(shas))
	for _, a := range shas {
		isTip := true
		for _, other := range shas {
			if a == other {
				continue
			}
			if _, err := b.gitRO(ctx, "merge-base", "--is-ancestor", a, other); err == nil {
				isTip = false
				break
			}
		}
		if isTip {
			tips = append(tips, a)
		}
	}
	return tips
}
