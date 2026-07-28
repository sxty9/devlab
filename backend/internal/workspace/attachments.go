// Attachment staging (B-6): a todo's media must actually REACH the agent. StageAttachments
// materializes each attachment via `devlab-exec write` below <wt>/.mercury-attachments/ (the
// runner-user workspace), returns a prompt manifest naming every staged file, and a cleanup
// that MUST run before stage/commit (and is additionally deferred) so attachment bytes never
// enter a commit.
package workspace

import "context"

// AttachmentFile is one attachment to stage: its filename and bytes.
type AttachmentFile struct {
	Filename string
	Data     []byte
	Note     string
}

// StagedAttachment names one materialized attachment inside the workspace.
type StagedAttachment struct {
	Filename string
	Path     string
	Note     string
}

// StageAttachments writes the files below <wt>/.mercury-attachments/ via devlab-exec write,
// returning the prompt manifest, the staged list and the mandatory cleanup.
func (e Executor) StageAttachments(ctx context.Context, repo string, files []AttachmentFile) (manifest string, staged []StagedAttachment, cleanup func() error, err error) {
	panic("TODO(B3)")
}
