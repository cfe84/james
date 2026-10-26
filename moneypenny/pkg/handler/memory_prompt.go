package handler

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/memory"
)

const memoryNodeMaxCharacters = memory.MaxBodyChars
const memoryRootTargetCharacters = memory.RootTargetChars
const memoryOversizedWarningCharacters = 2000

var errMemoryPreparation = errors.New("memory preparation failed")

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
- gadgets memory list [path] [--limit N] [--cursor TOKEN]: browse immediate topic summaries.
- gadgets memory search "query" [--limit N] [--cursor TOKEN]: search legacy notes; returns summaries, not full bodies.
- gadgets memory query [--text "words"] [--path topic] [--kind KIND] [--status STATUS] [--confidence LEVEL] [--importance LEVEL] [--method METHOD] [--source "reference"] [--verified-before RFC3339] [--stale=true|false] [--linked-to ID] [--relation RELATION] [--limit N] [--cursor TOKEN]: deterministic structured-claim retrieval; filters combine, path includes descendants.
- gadgets memory claim-get ID: retrieve the full claim, evidence, freshness and one-hop linked titles.
- gadgets memory claim-set: create or replace a structured claim from a JSON object on stdin; updates require id and current revision.
- gadgets memory claim-delete ID --revision N: permanent erasure only; use retained disproven/superseded claims for ordinary corrections.
- gadgets memory set [path]: replace a node with its complete body supplied on stdin (omit path for root).
- gadgets memory batch: atomically replace related notes and indexes using a JSON array [{"path":"topic","body":"complete note"}, ...] on stdin.
- gadgets memory delete path [--recursive]: remove obsolete nodes; preserve useful knowledge and update parent indexes.
- gadgets memory revisions [path]: inspect current node revisions before editing; re-read current content when needed.
Supply write bodies through stdin, not command-line arguments or temporary memory files.

Reading:
- Combine targeted search/query with hierarchical exploration for each memory request. Start from the root or relevant topic, search notes and claims, and inspect only promising branches/full claims.
- List/search/query return {items,has_more,next_cursor}: default 10, maximum 50 results and at most 16000 serialized Unicode characters including metadata. Keep the same filters when continuing with a cursor. Do not exhaust broad queries or follow every link automatically; narrow the topic or question first.
- Claims include categorical confidence, importance, provenance, verified_at/review_after, derived stale, and retained disputed/disproven/superseded status. Stale means review due, not false; missing verification is unknown, not fresh evidence. No automatic numerical confidence decay.
- Results include outgoing links and up to 16 backlinks with abbreviated titles, never recursive bodies. A shortened body has body_truncated=true: fetch claim-get before updating, as truncated-summary replacements are rejected. Check corrections/contradictions before relying on a claim; when more_backlinks is true, use query --linked-to ID, optionally --relation, to explore further.
- Ordinary query includes rejected and unresolved claims so corrections are not hidden. Do not filter only active claims when checking whether an old belief was disproven.
- The injected root is your big-picture overview and navigation map. Follow relevant links from broad topics to detailed notes; do not load the whole tree.
- If an index is empty or incomplete, use gadgets memory list to inspect its immediate children and repair the index before relying on it.
- Consult relevant memory before repeating prior investigation or relying on past decisions. Verify facts that may have changed against current sources.
- Re-read nodes when needed: the injected root is a snapshot from the start of this invocation.

Writing:
- Keep topic notes as summaries/navigation; put individually qualified knowledge in structured claims under an existing nonroot topic. Existing notes remain unqualified until deliberately reviewed; never invent confidence or provenance for imported text.
- Claim fields: path,title,body,kind (fact/decision/observation/question/negative_result), status (active/unresolved/disputed/disproven/superseded), confidence (unverified/tentative/supported), importance (critical/useful/incidental), provenance [{method:observed/inferred/user/document,source:"specific evidence",timestamp:"optional RFC3339"}], optional verified_at/review_after, and links [{target:"claim ID",relation:related_to/supports/contradicts/supersedes}].
- Example stdin for claim-set after creating topic engineering:
  {"path":"engineering","title":"Open deployment question","body":"Which host runs production? Check the deployment config before changing it.","kind":"question","status":"unresolved","confidence":"unverified","importance":"useful","provenance":[{"method":"inferred","source":"Deployment configuration not yet inspected"}],"links":[]}
- Keep titles within 200 and bodies within 4000 Unicode characters, 1-8 evidence entries (source up to 500 characters), and at most 16 outgoing links. Encoded non-link claim data is capped at 6000 characters so full retrieval always fits; split claims or shorten evidence if rejected. id,recorded_at,revision,stale,backlinks,more_backlinks and link titles are server-managed; retain id/revision when updating.
- Identify how something is known: direct observation, inference, user statement, or a specific document/turn. Preserve source references and actual verification dates; assistant repetition is not independent evidence and user attribution is not proof. Never mark an inference as observed or user-confirmed.
- Retain disproven/superseded claims, their reason/evidence, and links to corrections rather than deleting the old belief. Re-read before revision-checked updates; never overwrite a conflict blindly. Topic deletion is rejected while it still contains claims; move or explicitly erase those claims first.
- Keep a bounded uncertainty queue: at most 10 useful unresolved questions for the current task, recording what is unknown and the next verification action. Resolve or supersede answered questions. Record scoped negative results (where/when searched and what was not found), not universal claims of absence.
- Reduce uncertainty only when it affects the task: use an already-authorized check, or ask the user when necessary. Do not launch autonomous or endless exploration.
- Store only durable knowledge, relevant state, and navigation in memory nodes, not these memory-use instructions.
- Each node has a slash-delimited topic path (at most 1024 Unicode characters, 64 per segment), a concise summary, and an annotated index of its immediate children; the root path is empty.
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
		return fmt.Errorf("%w: load capabilities: %w", errMemoryPreparation, err)
	}
	if !capabilities.Memory {
		params.SystemPrompt += "\n\nPersistent session memory is disabled. Do not read or write session memory through gadgets, native file tools, or direct database access, even if earlier instructions mention memory."
		return h.preparePonsonbyInstructions(sessionID, params)
	}
	memDir := h.memoryDir(sessionID)
	if memDir == "" {
		return fmt.Errorf("%w: session memory directory unavailable", errMemoryPreparation)
	}
	prompt, err := memorySystemPrompt(memDir)
	if err != nil {
		return fmt.Errorf("%w: %w", errMemoryPreparation, err)
	}
	params.SystemPrompt += prompt
	return h.preparePonsonbyInstructions(sessionID, params)
}
