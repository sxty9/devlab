// Package model holds the JSON response shapes the DevLab API returns. Field names match
// the TypeScript types in frontend src/types.ts exactly, so the UI consumes them unchanged.
package model

// Repo is a selectable repository/service (discovered from GitHub topic "holistic").
type Repo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"` // service | repo | library
	Description string `json:"description"`
	Language    string `json:"language"`
	Tint        string `json:"tint"` // accent | success | warning | gpu | net | ssd | ram
}

// Branch is a git branch with tracking info.
type Branch struct {
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Updated   string `json:"updated"`
}

// FileNode is a node in the project file tree (id == repo-relative path).
type FileNode struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Kind     string     `json:"kind"` // file | dir
	Children []FileNode `json:"children,omitempty"`
	Lang     string     `json:"lang,omitempty"`
	Status   string     `json:"status,omitempty"` // git status decoration
}

// FileContent is a file's text + Monaco language id.
type FileContent struct {
	Path string `json:"path"`
	Lang string `json:"lang"`
	Code string `json:"code"`
}

// Change is a row in the Version Control panel.
type Change struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // modified | added | deleted | untracked | renamed | conflict
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Staged    bool   `json:"staged"`
}

// Diff is the before/after a DiffEditor renders.
type Diff struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Lang   string `json:"lang"`
}

// CommitLine is one drawn segment in a commit-graph row.
type CommitLine struct {
	From int `json:"from"`
	To   int `json:"to"`
	Lane int `json:"lane"`
}

// Commit is a node in the Git log graph (lanes computed server-side).
type Commit struct {
	Hash    string       `json:"hash"`
	Message string       `json:"message"`
	Author  string       `json:"author"`
	Time    string       `json:"time"`
	Refs    []string     `json:"refs,omitempty"`
	DotLane int          `json:"dotLane"`
	Lines   []CommitLine `json:"lines"`
}

// Worktree is a git worktree (IntelliJ-style management).
type Worktree struct {
	Branch  string `json:"branch"`
	Note    string `json:"note"`
	URL     string `json:"url,omitempty"`
	Current bool   `json:"current"`
}

// VisionDoc is a "Vision Deposit" artifact.
type VisionDoc struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"` // spec | mindmap | jet | note
	Summary string `json:"summary"`
	State   string `json:"state"` // done | active | pending
	Updated string `json:"updated"`
}

// Stage is a delivery-pipeline stage.
type Stage struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	State string `json:"state"`
	Hint  string `json:"hint"`
}

// Tab is an editor tab the workspace opens by default.
type Tab struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"` // code | structure | diff
	Path  string `json:"path,omitempty"`
	Lang  string `json:"lang,omitempty"`
	Dirty bool   `json:"dirty,omitempty"`
}

// StructureEntry is a row in a StructureSection.
type StructureEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // dir | file
	Note string `json:"note"`
}

// StructureSection is a section of the repo "skeleton" overview.
type StructureSection struct {
	Title   string           `json:"title"`
	Hint    string           `json:"hint"`
	Entries []StructureEntry `json:"entries"`
}

// RepoData is everything the workspace knows about one repo.
type RepoData struct {
	Branches    []Branch               `json:"branches"`
	Tree        []FileNode             `json:"tree"`
	Files       map[string]FileContent `json:"files"`
	DiffBefore  map[string]string      `json:"diffBefore,omitempty"`
	Changes     []Change               `json:"changes"`
	Commits     []Commit               `json:"commits"`
	Worktrees   []Worktree             `json:"worktrees"`
	Vision      []VisionDoc            `json:"vision"`
	Claude      []ClaudeMsg            `json:"claude"`
	Terminal    []TermLine             `json:"terminal"`
	Stages      []Stage                `json:"stages"`
	DefaultTabs []Tab                  `json:"defaultTabs"`
	ActiveTabID string                 `json:"activeTabId"`
	Structure   []StructureSection     `json:"structure"`
}

// ClaudeMsg is a message in the Claude transcript (live via WS in phase 2d).
type ClaudeMsg struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
	Tool string `json:"tool,omitempty"`
	Ts   string `json:"ts"`
}

// TermLine is a terminal scrollback line (live via WS in phase 2c).
type TermLine struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Preview is a live sxgate preview deployment.
type Preview struct {
	Slug    string `json:"slug"`
	Branch  string `json:"branch"`
	Service string `json:"service"`
	URL     string `json:"url"`
	State   string `json:"state"`
}
