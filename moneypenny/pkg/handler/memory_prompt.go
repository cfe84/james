package handler

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/memory"
)

const memoryNodeMaxCharacters = memory.MaxBodyChars
const memoryRootTargetCharacters = memory.RootTargetChars
const memoryOversizedWarningCharacters = 2000

const rootReadmeTemplate = `# Session Memory

No durable knowledge recorded yet.

## Topics

No topics recorded yet.
`

// Inject only the authored root, not a recursive outline or descendant bodies.
func memorySystemPrompt(memDir string) (string, error) {
	node, err := memory.Read(memDir, "", 0, memoryNodeMaxCharacters+1)
	if err != nil {
		return "", fmt.Errorf("read root memory: %w", err)
	}
	if node == nil {
		if _, err := memory.Set(memDir, "", rootReadmeTemplate); err != nil {
			return "", fmt.Errorf("seed root memory: %w", err)
		}
		node = &memory.Page{Node: &memory.Node{Body: rootReadmeTemplate}}
	}
	nodes, err := memory.Oversized(memDir)
	if err != nil {
		return "", fmt.Errorf("inspect oversized memory: %w", err)
	}
	root := []rune(node.Body)
	notice := ""
	if len(root) > memoryNodeMaxCharacters {
		root = root[:memoryNodeMaxCharacters]
		notice = fmt.Sprintf("\n[Root excerpt truncated at %d Unicode characters. Use gadgets memory get to read the full root before restructuring; stored content has not been changed.]\n", memoryNodeMaxCharacters)
	}
	return fmt.Sprintf(`

<session-memory>
Persistent memory is authoritative in the daemon's session-scoped SQLite store and survives session compactions and restarts.
Use ONLY gadgets memory commands for memory access. Do not use native file tools, filesystem paths, or direct database access for memory, including legacy README files.

Commands:
- gadgets memory get [path] [--offset N] [--limit N]: read a node; omit path for the root. Large imported notes return next_offset: continue from that Unicode-character offset until all relevant content is read.
- gadgets memory list [path]: inspect immediate children to navigate or repair an index.
- gadgets memory search "query": find relevant knowledge without loading the whole tree.
- gadgets memory set [path]: replace a node with its complete body supplied on stdin (omit path for root).
- gadgets memory batch: atomically replace related notes and indexes using a JSON array [{"path":"topic","body":"complete note"}, ...] on stdin.
- gadgets memory delete path [--recursive]: remove obsolete nodes; preserve useful knowledge and update parent indexes.
- gadgets memory revisions [path]: inspect current node revisions before editing; re-read current content when needed.
Supply write bodies through stdin, not command-line arguments or temporary memory files.

Reading:
- The injected root is your big-picture overview and navigation map. Follow relevant links from broad topics to detailed notes; do not load the whole tree.
- If an index is empty or incomplete, use gadgets memory list to inspect its immediate children and repair the index before relying on it.
- Consult relevant memory before repeating prior investigation or relying on past decisions. Verify facts that may have changed against current sources.
- Re-read nodes when needed: the injected root is a snapshot from the start of this invocation.

Writing:
- Store only durable knowledge, relevant state, and navigation in memory nodes, not these memory-use instructions.
- Each node has a slash-delimited topic path, a concise summary, and an annotated index of its immediate children; the root path is empty.
- Each index entry must identify the child's node path and explain what it contains and when to consult it. The root must cover every top-level knowledge area through this hierarchy.
- Keep EVERY node at most %d Unicode characters, including headings and its index. Aim for %d or fewer in the root.
- Before a note exceeds the limit, split its details into focused child nodes and replace the moved material with a summary and annotated index. Apply this recursively; group large indexes into intermediate topic nodes.
- Imported oversized notes are preserved intact, but replacements must obey the limit. When relevant, read the full note and restructure it while retaining useful knowledge and updating indexes. Do not silently truncate or discard knowledge.
- Update existing notes rather than duplicating information. Update the parent index whenever children are added, moved, renamed, or removed; update ancestor summaries when their scope changes.
- Keep the root a short overview and topic index, not a transcript or changelog. Put detailed history, evidence, and procedures in children.
- Update memory when durable decisions, findings, conventions, or relevant state change, and before handoff or compaction. Remove or clearly supersede obsolete information.

<root-memory>
%s
</root-memory>
%s%s</session-memory>
`, memoryNodeMaxCharacters, memoryRootTargetCharacters, string(root), notice, oversizedMemoryWarning(nodes)), nil
}

func oversizedMemoryWarning(nodes []memory.Size) string {
	total, shown := 0, 0
	var listing strings.Builder
	for _, node := range nodes {
		count := node.Characters
		if count <= memoryNodeMaxCharacters {
			continue
		}
		total++
		path := node.Path
		if path == "" {
			path = "(root)"
		}
		if runes := []rune(path); len(runes) > 160 {
			path = string(runes[:160]) + "…"
		}
		line := fmt.Sprintf("- %q: %d Unicode characters\n", path, count)
		// Reserve room for the heading and omitted-count/navigation footer.
		if utf8.RuneCountInString(listing.String())+utf8.RuneCountInString(line) <= memoryOversizedWarningCharacters-400 {
			listing.WriteString(line)
			shown++
		}
	}
	if total == 0 {
		return ""
	}
	footer := ""
	if shown < total {
		footer = fmt.Sprintf("%d additional oversized nodes omitted from this bounded warning; use gadgets memory list/get to inspect relevant branches.\n", total-shown)
	}
	return fmt.Sprintf("\nOversized imported memory nodes (%d total; limit %d per node):\n%s%sWhen relevant, retain useful knowledge while splitting these notes and updating indexes.\n", total, memoryNodeMaxCharacters, listing.String(), footer)
}

func (h *Handler) prepareRunInstructions(sessionID string, params *agent.RunParams) error {
	if err := h.prepareGadgets(sessionID, params); err != nil {
		return err
	}
	capabilities, err := h.gadgetCapabilities(sessionID)
	if err != nil {
		return fmt.Errorf("prepare memory capabilities: %w", err)
	}
	if !capabilities.Memory {
		params.SystemPrompt += "\n\nPersistent session memory is disabled. Do not read or write session memory through gadgets, native file tools, or direct database access, even if earlier instructions mention memory."
		return nil
	}
	if err := h.MigrateSessionMemoryToSQLite(sessionID); err != nil {
		return fmt.Errorf("prepare memory migration: %w", err)
	}
	memDir := h.memoryDir(sessionID)
	if memDir == "" {
		return fmt.Errorf("prepare memory: session memory directory unavailable")
	}
	prompt, err := memorySystemPrompt(memDir)
	if err != nil {
		return err
	}
	params.SystemPrompt += prompt
	return nil
}
