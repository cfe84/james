package handler

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
)

const memoryNodeMaxCharacters = 4000
const memoryRootTargetCharacters = 2000

const rootReadmeTemplate = `# Session Memory

No durable knowledge recorded yet.

## Topics

No topics recorded yet.
`

func seedRootReadme(memDir string) error {
	if err := os.MkdirAll(memDir, 0700); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(memDir, "README.md"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create root memory: %w", err)
	}
	_, writeErr := io.WriteString(f, rootReadmeTemplate)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("seed root memory: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close root memory: %w", closeErr)
	}
	return nil
}

// Inject only the authored root, not a recursive outline or descendant bodies.
func memorySystemPrompt(memDir string) (string, error) {
	if err := seedRootReadme(memDir); err != nil {
		return "", err
	}
	rootPath := filepath.Join(memDir, "README.md")
	f, err := os.Open(rootPath)
	if err != nil {
		return "", fmt.Errorf("open root memory: %w", err)
	}
	defer f.Close()
	// One extra character detects overflow even for four-byte Unicode text.
	content, err := io.ReadAll(io.LimitReader(f, int64((memoryNodeMaxCharacters+1)*utf8.UTFMax)))
	if err != nil {
		return "", fmt.Errorf("read root memory: %w", err)
	}
	root := []rune(string(content))
	notice := ""
	if len(root) > memoryNodeMaxCharacters {
		root = root[:memoryNodeMaxCharacters]
		notice = fmt.Sprintf("\n[Root excerpt truncated at %d characters. Read the full file at %s before restructuring it into smaller nodes; the file has not been changed.]\n", memoryNodeMaxCharacters, rootPath)
	}
	return fmt.Sprintf(`

<session-memory>
Persistent memory directory: %s
Root index: %s
This memory survives session compactions and restarts. Use your native file tools to read and edit it.

Reading:
- The injected root is your big-picture overview and navigation map. Follow relevant links from broad topics to detailed notes; do not load the whole tree.
- If an index is empty or incomplete, inspect that folder's immediate children and repair the index before relying on it.
- Consult relevant memory before repeating prior investigation or relying on past decisions. Verify facts that may have changed against current sources.
- Re-read files when needed: the injected root is a snapshot from the start of this invocation.

Writing:
- Store only durable knowledge, relevant state, and navigation in memory files, not these memory-use instructions.
- Each node is a folder containing README.md: a concise topic summary and an annotated index of its immediate children.
- Each index entry must link to the child's README.md and explain what it contains and when to consult it. The root must cover every top-level knowledge area through this hierarchy.
- Keep EVERY README at most %d Unicode characters, including headings and its index. Aim for %d or fewer in the root.
- Before a note exceeds the limit, split its details into focused child nodes and replace the moved material with a summary and annotated index. Apply this recursively; group large indexes into intermediate topic nodes.
- When you encounter an existing oversized note, flag it and restructure it without losing durable knowledge. Do not silently truncate files.
- Update existing notes rather than duplicating information. Update the parent index whenever children are added, moved, renamed, or removed; update ancestor summaries when their scope changes.
- Keep the root a short overview and topic index, not a transcript or changelog. Put detailed history, evidence, and procedures in children.
- Update memory when durable decisions, findings, conventions, or relevant state change, and before handoff or compaction. Remove or clearly supersede obsolete information.

<root-memory>
%s
</root-memory>
%s</session-memory>
`, memDir, rootPath, memoryNodeMaxCharacters, memoryRootTargetCharacters, string(root), notice), nil
}

func (h *Handler) prepareRunInstructions(sessionID string, params *agent.RunParams) error {
	params.SystemPrompt += notifyUserSystemPromptSuffix
	params.MemoryDir = ""
	if !agent.MemoryEnabled(params.Agent, params.Yolo) {
		return nil
	}
	h.ensureMemoryMigrated(sessionID)
	memDir := h.memoryDir(sessionID)
	if memDir == "" {
		return fmt.Errorf("prepare memory: session memory directory unavailable")
	}
	prompt, err := memorySystemPrompt(memDir)
	if err != nil {
		return err
	}
	params.MemoryDir = memDir
	params.SystemPrompt += prompt
	return nil
}
