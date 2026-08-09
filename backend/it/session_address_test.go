package it

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The way into a run's session must carry NO address: no host, no port, no scheme, no machine's
// path. A session is reached on the caller's own origin, and where the conversation itself lives
// is decided inside the service and never travels in a request.
//
// This is a SEARCH over the sources rather than a check of one file, because that is the property:
// an address introduced anywhere on this path — a client URL, a configurable endpoint, a host in a
// comment that later becomes code — breaks it. It walks the same source set the vocabulary test
// walks (the service's own Go and TypeScript), so a new file is covered without anyone remembering.
func TestNoAddressOnTheWayToASession(t *testing.T) {
	// What marks a line as belonging to this path.
	onTheWay := regexp.MustCompile(`mercuryRunSession|results/\{rid\}/session|/session\?|SpeakIntoSession|openSessions|TopicSession|runSessionRead|runSessionSpeak`)
	// What would be an address: a scheme, an IP, a port, a dotted hostname, or an absolute path on
	// some machine.
	address := regexp.MustCompile(`[a-z]+://|\b\d{1,3}(\.\d{1,3}){3}\b|:\d{4,5}\b|\b[a-z0-9-]+\.(com|org|net|io|dev|local|internal)\b|["'` + "`" + `]/(etc|opt|var|home|usr)/`)

	var offenders []string
	for _, path := range sourceFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if onTheWay.MatchString(line) && address.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
			}
		}
	}
	if len(offenders) > 0 {
		t.Errorf("the way to a session names an address (%d):\n  %s", len(offenders), strings.Join(offenders, "\n  "))
	}
}

// The client reaches a session on its OWN origin: the request carries a path and nothing else.
// This is the positive half — the search above proves no address appears, this proves the way
// exists at all and is relative.
func TestTheSessionIsReachedOnTheCallersOwnOrigin(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "src", "data", "httpSource.ts"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "results/${enc(resultId)}/session") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	if len(lines) == 0 {
		t.Fatal("no client call reaches a session at all — the surface would have nothing to read")
	}
	for _, line := range lines {
		if strings.Contains(line, "://") {
			t.Errorf("the session call carries an origin instead of a path: %s", line)
		}
		if !strings.Contains(line, "`/api/mercury/runs/") {
			t.Errorf("the session call does not start at the caller's own root: %s", line)
		}
	}
}
