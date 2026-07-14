// Package preview drives per-user branch previews. DevLab never runs preview infrastructure
// itself: it invokes the root, branch-code-free devlab-preview wrapper (which drives sxgate's
// preview engine with every untrusted step dropped to the requesting Linux user), and reads
// sxgate's instance metadata for listing. See deploy/PREVIEW-peruser-plan.md.
package preview

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	wrapper      = "/usr/local/sbin/devlab-preview"
	instancesDir = "/etc/sxgate/preview/instances"
	buildTimeout = "300" // seconds; hard-bounds a hostile build (review #1/#5)
	maxPerUser   = 5      // review #4: cap previews per user (shared port pool / host resources)
)

var (
	urlRe = regexp.MustCompile(`https://[A-Za-z0-9.-]+`)
	pwRe  = regexp.MustCompile(`password:\s*(\S+)`)
)

// Preview is one live per-user preview as surfaced to the SPA.
type Preview struct {
	Slug     string `json:"slug"`
	Branch   string `json:"branch"`
	Service  string `json:"service,omitempty"`
	URL      string `json:"url"`
	Password string `json:"password,omitempty"` // the shareable passphrase (login user: preview)
}

// Up creates (or fails cleanly) a preview of the user's branch and returns its URL + passphrase,
// parsed from the wrapper output. The user is the authenticated session user (never client input).
func Up(ctx context.Context, user, repoID, branch string) (Preview, error) {
	if countUser(user) >= maxPerUser {
		return Preview{}, errors.New("preview limit reached — remove one of your previews first")
	}
	cmd := exec.CommandContext(ctx, "sudo", "-n", wrapper, "up", user, repoID, branch)
	cmd.Env = append(os.Environ(), "DEVLAB_PV_TIMEOUT="+buildTimeout)
	out, err := cmd.CombinedOutput()
	s := string(out)
	if err != nil {
		return Preview{}, errors.New(trimErr(s, err))
	}
	p := Preview{Branch: branch, URL: urlRe.FindString(s)}
	if m := pwRe.FindStringSubmatch(s); m != nil {
		p.Password = m[1]
	}
	p.Slug = slugFromURL(p.URL)
	return p, nil
}

// Down tears the preview down (as its owner; the wrapper re-checks ownership).
func Down(ctx context.Context, user, slug string) error {
	cmd := exec.CommandContext(ctx, "sudo", "-n", wrapper, "down", user, slug)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(trimErr(string(out), err))
	}
	return nil
}

// List returns the user's previews for repoID, read from sxgate's instance metas (group-readable),
// filtered by OWNER and the workspace REPO path. Best-effort: unreadable/foreign metas are skipped.
func List(user, repoID string) []Preview {
	metas, _ := filepath.Glob(instancesDir + "/*.meta")
	var out []Preview
	for _, mf := range metas {
		m := readKV(mf)
		if m["OWNER"] != user || filepath.Base(m["REPO"]) != repoID {
			continue
		}
		p := Preview{Slug: m["SLUG"], Branch: m["BRANCH"], Service: m["SERVICE"]}
		if h := m["HOST"]; h != "" {
			p.URL = "https://" + h
		}
		// The passphrase is 0640 root:devlab — readable by the devlab service to show the user.
		if b, err := os.ReadFile(filepath.Join(instancesDir, p.Slug+".pw")); err == nil {
			p.Password = strings.TrimSpace(string(b))
		}
		out = append(out, p)
	}
	return out
}

// countUser counts a user's live previews across all repos (for the per-user cap).
func countUser(user string) int {
	metas, _ := filepath.Glob(instancesDir + "/*.meta")
	n := 0
	for _, mf := range metas {
		if readKV(mf)["OWNER"] == user {
			n++
		}
	}
	return n
}

// readKV parses a KEY=VALUE file (sxgate meta) into a map. Non-executing.
func readKV(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '='); i > 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m
}

// slugFromURL extracts the first DNS label of the preview host (the slug).
func slugFromURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	if i := strings.IndexByte(u, '.'); i > 0 {
		return u[:i]
	}
	return ""
}

// trimErr returns the most useful one-line error from the wrapper's combined output.
func trimErr(out string, err error) string {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			if len(ln) > 240 {
				ln = ln[:240] + "…"
			}
			return ln
		}
	}
	return err.Error()
}
