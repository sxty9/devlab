// Package deploy is the delivery-to-host machinery (S11): honest detection, build AS THE USER
// (root never builds), install-only via the pinned root wrapper, an honest running gate, gap
// detection, the self-repo handover (B-2) and the prod send (implemented, fixture-tested, NOT
// armed in this phase). devlabd itself NEVER calls systemd-run — the handover lives in the
// root wrapper.
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"devlab/backend/internal/model"
)

// Kind classifies a repo for delivery.
type Kind string

const (
	KindService       Kind = "service"
	KindLibrary       Kind = "library"
	KindExcluded      Kind = "excluded"
	KindNonconforming Kind = "nonconforming"
	KindTemplate      Kind = "template"
)

// DeclarationFileName is the optional per-repo declaration (REQ-028.2): named values instead of
// code, in the repo root. It is the ONLY way a repo states delivery deviations — there are no
// per-repo scripts (B-44).
const DeclarationFileName = "holistic-service.json"

// templatePlaceholderID is the pristine template's placeholder service id: a repo still carrying
// permissions/<placeholder>.json IS the template and never counts as a delivery gap (REQ-029.4).
const templatePlaceholderID = "myservice"

// Detection is a classification WITH its evidence: the conforming ./service CLI (template
// convention) or backend/cmd/<id>d for a service; the declaration file for excluded.
type Detection struct {
	Kind     Kind
	Evidence string
	// ID is the detected service id (unit and route stem): the permissions-manifest stem, the
	// cmd/<id>d stem, or the repo directory name. Empty for non-services.
	ID string
	// Decl is the parsed declaration file, when present.
	Decl *DeclarationFile
}

// DeclarationFile is the optional per-repo declaration (REQ-028.2 — named values instead of
// code): holistic-service.json in the repo root.
type DeclarationFile struct {
	Deliver  *bool  `json:"deliver,omitempty"`
	Port     int    `json:"port,omitempty"`
	Health   string `json:"health,omitempty"`
	Artifact *struct {
		Binary string `json:"binary,omitempty"`
		Web    string `json:"web,omitempty"`
	} `json:"artifact,omitempty"`
}

// Detect classifies one repo directory with evidence. Order of judgement: the pristine template,
// an explicit exclusion, a conforming service (the ./service CLI or a cmd/<id>d daemon), a repo
// that CLAIMS delivery without conforming, and — only then — a library.
func Detect(repoDir string) (Detection, error) {
	if fi, err := os.Stat(repoDir); err != nil || !fi.IsDir() {
		return Detection{}, fmt.Errorf("deploy: not a repo directory: %s", repoDir)
	}

	decl, declErr := readDeclaration(repoDir)

	// 1) The pristine template: still carries the placeholder id — never a service, never a gap.
	if _, err := os.Stat(filepath.Join(repoDir, "permissions", templatePlaceholderID+".json")); err == nil {
		return Detection{Kind: KindTemplate, Decl: decl,
			Evidence: "pristine service template (placeholder permissions/" + templatePlaceholderID + ".json)"}, nil
	}

	// 2) A malformed declaration is a violation, not a guess.
	if declErr != nil {
		return Detection{Kind: KindNonconforming,
			Evidence: DeclarationFileName + " is unreadable: " + declErr.Error()}, nil
	}

	// 3) Explicitly declared not-to-deliver: abbildbar, and it suppresses every attempt.
	if decl != nil && decl.Deliver != nil && !*decl.Deliver {
		return Detection{Kind: KindExcluded, Decl: decl,
			Evidence: DeclarationFileName + " declares deliver:false"}, nil
	}

	// 4) A conforming service: the uniform ./service CLI, or the template daemon layout.
	id := manifestID(repoDir)
	if fi, err := os.Stat(filepath.Join(repoDir, "service")); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		if id == "" {
			id = filepath.Base(repoDir)
		}
		return Detection{Kind: KindService, ID: id, Decl: decl,
			Evidence: "./service CLI (template convention)"}, nil
	}
	if cmdID, dir := daemonCmdID(repoDir); cmdID != "" {
		return Detection{Kind: KindService, ID: cmdID, Decl: decl,
			Evidence: dir + "/" + cmdID + "d (template daemon layout)"}, nil
	}

	// 5) Claims delivery (declaration present) or looks daemon-shaped, but conforms to nothing:
	// a "Code-Struktur" violation to report — never a special path (REQ-028.4).
	if decl != nil {
		return Detection{Kind: KindNonconforming, Decl: decl,
			Evidence: DeclarationFileName + " present, but neither a ./service CLI nor a cmd/<id>d daemon conforms to the shared structure"}, nil
	}
	if hasNonconformingCmd(repoDir) {
		return Detection{Kind: KindNonconforming,
			Evidence: "has a cmd/ tree, but neither a ./service CLI nor a cmd/<id>d daemon conforms to the shared structure"}, nil
	}

	// 6) Everything else holds code without a deliverable daemon: a library.
	return Detection{Kind: KindLibrary, Evidence: "no ./service CLI and no cmd/<id>d daemon — nothing to deliver"}, nil
}

func readDeclaration(repoDir string) (*DeclarationFile, error) {
	raw, err := os.ReadFile(filepath.Join(repoDir, DeclarationFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d DeclarationFile
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// manifestID returns the service id from permissions/<id>.json, the template's source of truth.
func manifestID(repoDir string) string {
	entries, err := os.ReadDir(filepath.Join(repoDir, "permissions"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
			return name
		}
	}
	return ""
}

// daemonCmdID finds the template daemon layout cmd/<id>d (repo root or backend/), returning the
// id and which cmd tree carried it.
func daemonCmdID(repoDir string) (id, dir string) {
	for _, base := range []string{"backend/cmd", "cmd"} {
		entries, err := os.ReadDir(filepath.Join(repoDir, base))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasSuffix(e.Name(), "d") && len(e.Name()) > 1 {
				return strings.TrimSuffix(e.Name(), "d"), base
			}
		}
	}
	return "", ""
}

func hasNonconformingCmd(repoDir string) bool {
	for _, base := range []string{"backend/cmd", "cmd"} {
		entries, err := os.ReadDir(filepath.Join(repoDir, base))
		if err != nil {
			continue
		}
		if len(entries) > 0 {
			return true
		}
	}
	return false
}

// Gap names a repo whose delivery is possible but not yet set up — "delivery not yet set up"
// is DISTINCT from "no service" and never green (REQ-029). Template repos do not count;
// excluded never triggers an attempt.
type Gap struct {
	Repo   string
	Kind   Kind
	Detail string
}

// FindGaps derives the delivery gaps from detections and observed ports: a detected service
// with no routed port has no delivery path yet (REQ-029.1); a nonconforming repo is reported as
// the "Code-Struktur" violation it is (REQ-028.4). Libraries, the template and excluded repos
// produce no gap.
func FindGaps(dets map[string]Detection, allocs []model.PortAllocation) []Gap {
	repos := make([]string, 0, len(dets))
	for r := range dets {
		repos = append(repos, r)
	}
	sort.Strings(repos)

	var gaps []Gap
	for _, repo := range repos {
		d := dets[repo]
		switch d.Kind {
		case KindService:
			if _, routed := routedPortOf(allocs, d.ID); !routed {
				gaps = append(gaps, Gap{Repo: repo, Kind: KindService,
					Detail: "delivery not yet set up: service detected (" + d.Evidence + ") but no route/port is provisioned"})
			}
		case KindNonconforming:
			gaps = append(gaps, Gap{Repo: repo, Kind: KindNonconforming,
				Detail: "violates the Code-Struktur axiom: " + d.Evidence})
		}
	}
	return gaps
}

func routedPortOf(allocs []model.PortAllocation, service string) (int, bool) {
	if service == "" {
		return 0, false
	}
	for _, a := range allocs {
		if a.Routed && a.Service == service {
			return a.Port, true
		}
	}
	return 0, false
}
