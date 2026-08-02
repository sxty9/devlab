// Package git shells out to the git CLI (read-only plumbing) and shapes the output into the
// model.* structs the DevLab UI consumes. No git library — just `git -C <repo> …`.
package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"devlab/backend/internal/model"
)

const maxFileBytes = 2 << 20 // 2 MiB editor cap

// run executes a read-only `git … -C repo args…` and returns trimmed stdout. Runs as the devlab
// service user directly (reads are fast, no sudo). --no-optional-locks keeps status/ls-files from
// taking .git/index.lock (so a concurrent user-run write doesn't collide). safe.directory=* lets the
// service read repos OWNED by a different user (the per-user workspace model), where git would
// otherwise refuse with "dubious ownership".
func run(repo string, args ...string) (string, error) {
	full := append([]string{"--no-optional-locks", "-c", "safe.directory=*", "-C", repo}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

// runRaw is run() WITHOUT the trailing-newline trim: it returns stdout byte-for-byte. Content whose
// checksum must match a file on disk (the root-wrapper renewal compares the committed blob against
// the installed wrapper) cannot be trimmed — a stripped final newline would change the hash.
func runRaw(repo string, args ...string) ([]byte, error) {
	full := append([]string{"--no-optional-locks", "-c", "safe.directory=*", "-C", repo}, args...)
	return exec.Command("git", full...).Output()
}

// FileAtRefBytes returns the committed content of a repo-relative path at a git ref (e.g.
// "origin/main"), byte-for-byte, and whether the path exists there. It reads committed HISTORY, never
// the working tree — the ref names a merged stand, which is the only admissible source for a
// root-wrapper renewal (only content that traversed the full chain and was merged). The bytes equal
// the blob, so their sha256 equals that of a file installed verbatim from the same blob.
func FileAtRefBytes(repo, ref, rel string) ([]byte, bool) {
	b, err := runRaw(repo, "show", ref+":"+rel)
	if err != nil {
		return nil, false
	}
	return b, true
}

// Lang maps a path to a Monaco language id (mirrors frontend src/lib/lang.ts).
func Lang(path string) string {
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	if name == "Dockerfile" {
		return "dockerfile"
	}
	if name == "sxgate" || strings.HasSuffix(name, ".conf") {
		return "shell"
	}
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		ext = name[i+1:]
	}
	switch ext {
	case "ts", "tsx":
		return "typescript"
	case "js", "jsx", "mjs", "cjs":
		return "javascript"
	case "go":
		return "go"
	case "py":
		return "python"
	case "rs":
		return "rust"
	case "sh", "bash":
		return "shell"
	case "json":
		return "json"
	case "css":
		return "css"
	case "scss":
		return "scss"
	case "html":
		return "html"
	case "md":
		return "markdown"
	case "yml", "yaml":
		return "yaml"
	case "toml":
		return "ini"
	default:
		return "plaintext"
	}
}

// DefaultBranch resolves the repo's default branch (origin/HEAD, else main/master, else current).
func DefaultBranch(repo string) string {
	if s, err := run(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && s != "" {
		return strings.TrimPrefix(s, "origin/")
	}
	for _, b := range []string{"main", "master"} {
		if _, err := run(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	if s, err := run(repo, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		return s
	}
	return "main"
}

// Branches lists local branches with tracking info.
func Branches(repo string) []model.Branch {
	def := DefaultBranch(repo)
	out, err := run(repo, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)%09%(committerdate:relative)", "refs/heads")
	if err != nil {
		return []model.Branch{}
	}
	var bs []model.Branch
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		name := parts[0]
		updated := ""
		if len(parts) > 1 {
			updated = parts[1]
		}
		ahead, behind := 0, 0
		if s, err := run(repo, "rev-list", "--left-right", "--count", name+"@{upstream}..."+name); err == nil {
			f := strings.Fields(s)
			if len(f) == 2 {
				behind, _ = strconv.Atoi(f[0])
				ahead, _ = strconv.Atoi(f[1])
			}
		}
		bs = append(bs, model.Branch{Name: name, IsDefault: name == def, Ahead: ahead, Behind: behind, Updated: updated})
	}
	if bs == nil {
		bs = []model.Branch{}
	}
	return bs
}

// Tree builds the working-tree file tree (tracked + untracked, gitignore-respected), with git
// status decorations merged in.
func Tree(repo string) []model.FileNode {
	out, err := run(repo, "ls-files", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return []model.FileNode{}
	}
	statusByPath := map[string]string{}
	for _, c := range Changes(repo) {
		// last write wins; staged status is fine as a decoration
		statusByPath[c.Path] = c.Status
	}
	root := &node{children: map[string]*node{}}
	for _, p := range strings.Split(out, "\n") {
		if p == "" {
			continue
		}
		root.insert(strings.Split(p, "/"), p)
	}
	return root.toModel("", statusByPath)
}

type node struct {
	children map[string]*node
	isFile   bool
	path     string
}

func (n *node) insert(parts []string, full string) {
	if len(parts) == 0 {
		return
	}
	name := parts[0]
	child, ok := n.children[name]
	if !ok {
		child = &node{children: map[string]*node{}}
		n.children[name] = child
	}
	if len(parts) == 1 {
		child.isFile = true
		child.path = full
	} else {
		child.insert(parts[1:], full)
	}
}

func (n *node) toModel(prefix string, status map[string]string) []model.FileNode {
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	// dirs first, then files; each alphabetical (case-insensitive)
	sort.Slice(names, func(i, j int) bool {
		ci, cj := n.children[names[i]], n.children[names[j]]
		if ci.isFile != cj.isFile {
			return !ci.isFile // dir before file
		}
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	out := make([]model.FileNode, 0, len(names))
	for _, name := range names {
		c := n.children[name]
		id := name
		if prefix != "" {
			id = prefix + "/" + name
		}
		if c.isFile {
			out = append(out, model.FileNode{ID: c.path, Name: name, Kind: "file", Lang: Lang(name), Status: status[c.path]})
		} else {
			out = append(out, model.FileNode{ID: id, Name: name, Kind: "dir", Children: c.toModel(id, status)})
		}
	}
	return out
}

// FileAt reads a working-tree file (path is repo-relative). Binary/oversized files return a note.
func FileAt(repo, rel string) model.FileContent {
	lang := Lang(rel)
	abs := filepath.Join(repo, filepath.Clean("/"+rel))
	if !strings.HasPrefix(abs, filepath.Clean(repo)+string(os.PathSeparator)) {
		return model.FileContent{Path: rel, Lang: lang, Code: "// (refused: path escapes repository)\n"}
	}
	// A committed symlink can point outside the repo; os.ReadFile would follow it. Resolve links
	// and confirm the target stays inside the (symlink-resolved) repo, mirroring the write path's
	// guard, so a malicious repo cannot exfiltrate arbitrary server-side files.
	if escapesViaSymlink(repo, abs) {
		return model.FileContent{Path: rel, Lang: lang, Code: "// (refused: path escapes repository)\n"}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return model.FileContent{Path: rel, Lang: lang, Code: "// (file not found)\n"}
	}
	if info.Size() > maxFileBytes {
		return model.FileContent{Path: rel, Lang: lang, Code: "// (file too large to display)\n"}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return model.FileContent{Path: rel, Lang: lang, Code: "// (could not read file)\n"}
	}
	if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		return model.FileContent{Path: rel, Lang: "plaintext", Code: "// (binary file)\n"}
	}
	return model.FileContent{Path: rel, Lang: lang, Code: string(b)}
}

// escapesViaSymlink reports whether abs resolves (through symlinks) to a location outside repo.
// It resolves the deepest existing ancestor of abs so a symlinked directory anywhere on the path
// is caught. Any resolution error on the repo root is treated as an escape (fail closed).
func escapesViaSymlink(repo, abs string) bool {
	root, err := filepath.EvalSymlinks(filepath.Clean(repo))
	if err != nil {
		return true
	}
	p := abs
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real != root && !strings.HasPrefix(real, root+string(os.PathSeparator))
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false // no existing ancestor (path simply doesn't exist yet) — os.Stat handles it
		}
		p = parent
	}
}

// showAt returns committed content of a path at a ref (empty if absent).
func showAt(repo, ref, rel string) string {
	s, err := run(repo, "show", ref+":"+rel)
	if err != nil {
		return ""
	}
	return s
}

// DiffFor returns HEAD↔working-tree before/after for a changed file.
func DiffFor(repo, rel string) model.Diff {
	before := showAt(repo, "HEAD", rel)
	after := FileAt(repo, rel).Code
	// A deleted file has no working copy; FileAt returns a "(file not found)" note — blank it.
	if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
		after = ""
	}
	return model.Diff{Before: before, After: after, Lang: Lang(rel)}
}

// visionKind classifies a vision file by extension for the catalog renderer.
func visionKind(name string) string {
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		ext = strings.ToLower(name[i+1:])
	}
	switch ext {
	case "png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "avif", "ico":
		return "image"
	case "pdf":
		return "pdf"
	case "md", "markdown":
		return "markdown"
	case "txt", "json", "yaml", "yml", "toml", "csv", "ts", "tsx", "js", "go", "py", "sh", "html", "css":
		return "text"
	default:
		return "other"
	}
}

// ListFiles returns the repo's tracked+untracked (gitignore-respected) file paths, flat.
func ListFiles(repo string) []string {
	out, err := run(repo, "ls-files", "--cached", "--others", "--exclude-standard")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// VisionFiles lists the tracked+untracked (gitignore-respected) files under the repo's vision/
// folder, classified for the Vision Catalog, with sizes and git-status decorations. Hidden files
// (e.g. the vision/.gitignore itself) are omitted.
func VisionFiles(repo string) []model.VisionFile {
	out, err := run(repo, "ls-files", "--cached", "--others", "--exclude-standard", "--", "vision")
	if err != nil {
		return []model.VisionFile{}
	}
	statusByPath := map[string]string{}
	for _, c := range Changes(repo) {
		statusByPath[c.Path] = c.Status
	}
	seen := map[string]bool{}
	var files []model.VisionFile
	for _, p := range strings.Split(out, "\n") {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		name := p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			name = p[i+1:]
		}
		if strings.HasPrefix(name, ".") {
			continue // hide .gitignore / dotfiles from the catalog
		}
		var size int64
		if fi, err := os.Stat(filepath.Join(repo, p)); err == nil {
			size = fi.Size()
		}
		files = append(files, model.VisionFile{Path: p, Name: name, Kind: visionKind(name), Size: size, Status: statusByPath[p]})
	}
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path) })
	if files == nil {
		files = []model.VisionFile{}
	}
	return files
}

func mapStatus(code byte) string {
	switch code {
	case 'M':
		return "modified"
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "modified"
	case 'U':
		return "conflict"
	default:
		return "modified"
	}
}

// Changes parses `git status --porcelain` into staged/unstaged Change rows with +/- counts.
func Changes(repo string) []model.Change {
	out, err := run(repo, "status", "--porcelain")
	if err != nil || out == "" {
		return []model.Change{}
	}
	unstaged := numstat(repo, false)
	staged := numstat(repo, true)
	var changes []model.Change
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 {
			continue
		}
		x, y := line[0], line[1]
		path := line[3:]
		if i := strings.Index(path, " -> "); i >= 0 { // rename: old -> new
			path = path[i+4:]
		}
		if x == '?' && y == '?' {
			changes = append(changes, model.Change{Path: path, Status: "untracked", Additions: lineCount(repo, path), Staged: false})
			continue
		}
		if x != ' ' && x != '?' {
			a, d := staged[path][0], staged[path][1]
			changes = append(changes, model.Change{Path: path, Status: mapStatus(x), Additions: a, Deletions: d, Staged: true})
		}
		if y != ' ' && y != '?' {
			a, d := unstaged[path][0], unstaged[path][1]
			changes = append(changes, model.Change{Path: path, Status: mapStatus(y), Additions: a, Deletions: d, Staged: false})
		}
	}
	if changes == nil {
		changes = []model.Change{}
	}
	return changes
}

func numstat(repo string, cached bool) map[string][2]int {
	args := []string{"diff", "--numstat"}
	if cached {
		args = append(args, "--cached")
	}
	out, err := run(repo, args...)
	m := map[string][2]int{}
	if err != nil {
		return m
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		a, _ := strconv.Atoi(f[0]) // "-" for binary → 0
		d, _ := strconv.Atoi(f[1])
		m[f[2]] = [2]int{a, d}
	}
	return m
}

func lineCount(repo, rel string) int {
	b, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// Worktrees lists git worktrees; `current` marks the queried checkout.
func Worktrees(repo string) []model.Worktree {
	out, err := run(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return []model.Worktree{}
	}
	var wts []model.Worktree
	var cur model.Worktree
	flush := func() {
		if cur.Note != "" {
			wts = append(wts, cur)
		}
		cur = model.Worktree{}
	}
	clean := filepath.Clean(repo)
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			p := strings.TrimPrefix(line, "worktree ")
			cur.Note = p
			cur.Current = filepath.Clean(p) == clean
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.Branch = "(detached)"
		case line == "":
			// record separator handled by next "worktree"
		}
	}
	flush()
	if wts == nil {
		wts = []model.Worktree{}
	}
	return wts
}

// Commits returns the log for a branch with server-computed graph lanes.
func Commits(repo, branch string, limit int) []model.Commit {
	if branch == "" {
		branch = DefaultBranch(repo)
	}
	if limit <= 0 {
		limit = 80
	}
	const us = "\x1f"
	format := strings.Join([]string{"%H", "%h", "%an", "%cr", "%P", "%D", "%s"}, us)
	out, err := run(repo, "log", branch, "--no-color", "-n", strconv.Itoa(limit), "--pretty=format:"+format)
	if err != nil || out == "" {
		return []model.Commit{}
	}
	var raws []rawCommit
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, us)
		if len(f) < 7 {
			continue
		}
		raws = append(raws, rawCommit{f[0], f[1], f[2], f[3], f[4], f[5], f[6]})
	}
	return layout(raws)
}

type rawCommit struct {
	full, short, author, time, parents, refs, subject string
}

// layout assigns graph lanes (newest→oldest) and builds the per-row line segments.
func layout(raws []rawCommit) []model.Commit {
	lanes := []string{} // lane index → full hash it currently expects
	free := func() int {
		for i, h := range lanes {
			if h == "" {
				return i
			}
		}
		lanes = append(lanes, "")
		return len(lanes) - 1
	}
	commits := make([]model.Commit, 0, len(raws))
	for _, r := range raws {
		top := append([]string(nil), lanes...)
		dot := -1
		for i, h := range top {
			if h == r.full {
				dot = i
				break
			}
		}
		if dot == -1 {
			dot = free()
		}
		for len(lanes) <= dot {
			lanes = append(lanes, "")
		}
		var lines []model.CommitLine
		seenDot := false
		for L, h := range top {
			if h == "" {
				continue
			}
			if h == r.full {
				lines = append(lines, model.CommitLine{From: L, To: dot, Lane: L})
				if L == dot {
					seenDot = true
				}
			} else {
				lines = append(lines, model.CommitLine{From: L, To: L, Lane: L})
			}
		}
		if !seenDot {
			lines = append(lines, model.CommitLine{From: dot, To: dot, Lane: dot})
		}
		// bottom state: clear lanes reaching this commit, then place parents
		for i := range lanes {
			if lanes[i] == r.full {
				lanes[i] = ""
			}
		}
		parents := strings.Fields(r.parents)
		if len(parents) > 0 {
			lanes[dot] = parents[0]
		} else {
			lanes[dot] = ""
		}
		for i := 1; i < len(parents); i++ {
			nl := free()
			lanes[nl] = parents[i]
			lines = append(lines, model.CommitLine{From: dot, To: nl, Lane: nl})
		}
		commits = append(commits, model.Commit{
			Hash: r.short, Message: r.subject, Author: r.author, Time: r.time,
			Refs: parseRefs(r.refs), DotLane: dot, Lines: lines,
		})
	}
	return commits
}

func parseRefs(d string) []string {
	if d == "" {
		return nil
	}
	var refs []string
	for _, part := range strings.Split(d, ", ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, " -> "); i >= 0 { // "HEAD -> main"
			refs = append(refs, strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+4:]))
			continue
		}
		part = strings.TrimPrefix(part, "tag: ")
		refs = append(refs, part)
	}
	return refs
}
