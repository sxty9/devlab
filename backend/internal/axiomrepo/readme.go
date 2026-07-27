package axiomrepo

import (
	"context"
	_ "embed"
	"errors"
)

//go:embed constitution_readme.md
var readmeContent string

// ReadmePath is the constitution repository's self-describing README, at the repo root. List skips it
// (see List), so it is documentation only and never surfaces as a constitution record.
const ReadmePath = "README.md"

// EnsureReadme seeds the repository's README once, so the constitution repo documents itself — where
// the constitution lives, how it is edited, and why it sits outside the protected code repos. It is
// create-only (an existing README, however edited, is left untouched) and best-effort: if the repo is
// unreachable or no account is linked it returns that error and writes nothing, never blocking a read.
func (s *Store) EnsureReadme(ctx context.Context) error {
	_, found, err := s.Get(ctx, ReadmePath)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if err := s.Put(ctx, ReadmePath, readmeContent, "docs: describe the constitution repository", "Mercury", false); err != nil && !errors.Is(err, ErrExists) {
		return err
	}
	return nil
}
