package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type diffMode int

const (
	diffModeView diffMode = iota
	diffModeCommitMsg
	diffModeLineInput    // typing a line number for r/d
	diffModeComment      // editing a review comment
	diffModeSubmitReview // submit review confirmation + overall prompt
	diffModeConfirmQuit  // quit with pending comments
)

type diffTab int

const (
	diffTabDiff diffTab = iota
	diffTabLog
	diffTabCommit // viewing a specific commit
)

// logEntry represents a parsed git log line with its commit hash.
type logEntry struct {
	line string // original line
	hash string // commit hash (empty for non-commit lines like graph connectors)
}

// diffLineMeta holds parsed metadata for each line in the diff output.
type diffLineMeta struct {
	file    string // file path (from +++ b/X)
	lineNum int    // real line number in the file (0 for header/hunk lines)
	side    string // "+", "-", " " (context), "" (header/hunk)
	code    string // the code content (without +/- prefix)
}

// reviewComment holds a comment on a specific diff line.
type reviewComment struct {
	seqLine int    // sequential line number (1-based)
	comment string // the comment text
}

// diffReviewSubmitMsg is emitted when the user submits review comments.
// ui.go handles this to switch to chat and send the prompt.
type diffReviewSubmitMsg struct {
	sessionID string
	prompt    string
}

// diffModel displays a git diff for a session.
type diffModel struct {
	sessionID string
	diff      string
	gitLog    string
	branch    string
	tab       diffTab
	prevTab   diffTab // tab to return to when leaving commit view
	scroll    int
	logScroll int
	width     int
	height    int
	err       error
	loading   bool
	logLoading bool
	client    *client

	mode       diffMode
	commitMsg  string
	pushAfter  bool // if true, push after commit
	amendMode  bool // if true, amend last commit instead of new commit
	committing bool
	commitErr  error

	// Log selection.
	logEntries  []logEntry
	logCursor   int

	// Commit detail view.
	commitDetail  string
	commitHash    string
	commitScroll  int
	commitLoading bool
	commitErr2    error

	// Diff line metadata (parsed from diff output).
	lineMeta []diffLineMeta

	// Review comments.
	comments     map[int]*reviewComment // keyed by sequential line number (1-based)
	lineInput    textInput              // for line number entry (r/d)
	commentInput textInput              // for editing a comment
	reviewPrompt textInput              // for overall review prompt
	lineAction   string                 // "comment" or "delete"
	pendingLine  int                    // line number being commented
}

type diffLoadedMsg struct {
	diff string
	err  error
}

type gitLogLoadedMsg struct {
	log string
	err error
}

type gitInfoLoadedMsg struct {
	branch string
	err    error
}

type diffCommitDoneMsg struct {
	pushed bool
	err    error
}

type diffPushDoneMsg struct {
	err error
}

type gitShowLoadedMsg struct {
	show string
	hash string
	err  error
}

func newDiffModel(c *client, sessionID string) diffModel {
	return diffModel{
		client:       c,
		sessionID:    sessionID,
		loading:      true,
		logLoading:   true,
		comments:     make(map[int]*reviewComment),
		lineInput:    newTextInput(false),
		commentInput: newTextInput(true),
		reviewPrompt: newTextInput(true),
	}
}

func (m diffModel) loadDiff() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		resp, err := m.client.send("diff", "session", sessionID)
		if err != nil {
			return diffLoadedMsg{err: err}
		}
		if resp.Status == "error" {
			return diffLoadedMsg{err: fmt.Errorf("%s", resp.Message)}
		}
		var result struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			return diffLoadedMsg{err: fmt.Errorf("parsing diff: %w", err)}
		}
		return diffLoadedMsg{diff: result.Message}
	}
}

func (m diffModel) loadGitLog() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		log, err := m.client.gitLog(sessionID)
		return gitLogLoadedMsg{log: log, err: err}
	}
}

func (m diffModel) loadGitInfo() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		branch, err := m.client.gitInfo(sessionID)
		return gitInfoLoadedMsg{branch: branch, err: err}
	}
}

func (m diffModel) loadGitShow(hash string) tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		show, err := m.client.gitShow(sessionID, hash)
		return gitShowLoadedMsg{show: show, hash: hash, err: err}
	}
}

func (m diffModel) doCommit() tea.Cmd {
	sessionID := m.sessionID
	msg := m.commitMsg
	push := m.pushAfter
	amend := m.amendMode
	return func() tea.Msg {
		var err error
		if amend {
			err = m.client.amendSession(sessionID, msg)
		} else {
			err = m.client.commitSession(sessionID, msg)
		}
		if err != nil {
			return diffCommitDoneMsg{err: err}
		}
		if push {
			if amend {
				err = m.client.forcePushSession(sessionID)
			} else {
				err = m.client.pushSession(sessionID)
			}
			return diffCommitDoneMsg{pushed: true, err: err}
		}
		return diffCommitDoneMsg{}
	}
}

func (m diffModel) doPush() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		err := m.client.pushSession(sessionID)
		return diffPushDoneMsg{err: err}
	}
}

// parseDiffMeta parses diff output and returns metadata for each line.
func parseDiffMeta(diff string) []diffLineMeta {
	lines := strings.Split(diff, "\n")
	meta := make([]diffLineMeta, len(lines))
	var currentFile string
	var oldLine, newLine int

	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			currentFile = strings.TrimPrefix(line, "+++ b/")
			meta[i] = diffLineMeta{file: currentFile, side: ""}
		case strings.HasPrefix(line, "+++ "):
			// +++ /dev/null or similar
			meta[i] = diffLineMeta{file: currentFile, side: ""}
		case strings.HasPrefix(line, "--- "):
			meta[i] = diffLineMeta{file: currentFile, side: ""}
		case strings.HasPrefix(line, "diff "):
			// diff --git a/X b/Y — extract file from b/Y
			if idx := strings.Index(line, " b/"); idx >= 0 {
				currentFile = line[idx+3:]
			}
			meta[i] = diffLineMeta{file: currentFile, side: ""}
		case strings.HasPrefix(line, "@@"):
			// Parse hunk header: @@ -old,count +new,count @@
			oldLine, newLine = parseHunkHeader(line)
			meta[i] = diffLineMeta{file: currentFile, side: ""}
		case strings.HasPrefix(line, "+"):
			meta[i] = diffLineMeta{
				file:    currentFile,
				lineNum: newLine,
				side:    "+",
				code:    strings.TrimPrefix(line, "+"),
			}
			newLine++
		case strings.HasPrefix(line, "-"):
			meta[i] = diffLineMeta{
				file:    currentFile,
				lineNum: oldLine,
				side:    "-",
				code:    strings.TrimPrefix(line, "-"),
			}
			oldLine++
		case len(line) > 0 && line[0] == ' ':
			meta[i] = diffLineMeta{
				file:    currentFile,
				lineNum: newLine,
				side:    " ",
				code:    line[1:],
			}
			oldLine++
			newLine++
		default:
			// index, mode, or other header lines
			meta[i] = diffLineMeta{file: currentFile, side: ""}
		}
	}
	return meta
}

// parseHunkHeader parses @@ -old,count +new,count @@ and returns old/new start lines.
func parseHunkHeader(line string) (oldStart, newStart int) {
	// Format: @@ -10,5 +12,7 @@ optional text
	line = strings.TrimPrefix(line, "@@")
	idx := strings.Index(line, "@@")
	if idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)

	parts := strings.Fields(line)
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			p = strings.TrimPrefix(p, "-")
			if comma := strings.Index(p, ","); comma >= 0 {
				p = p[:comma]
			}
			n, _ := strconv.Atoi(p)
			oldStart = n
		} else if strings.HasPrefix(p, "+") {
			p = strings.TrimPrefix(p, "+")
			if comma := strings.Index(p, ","); comma >= 0 {
				p = p[:comma]
			}
			n, _ := strconv.Atoi(p)
			newStart = n
		}
	}
	return
}

// buildReviewPrompt generates the review prompt from comments.
func (m diffModel) buildReviewPrompt(overallComment string) string {
	var b strings.Builder

	if strings.TrimSpace(overallComment) != "" {
		b.WriteString(overallComment)
		b.WriteString("\n\n")
	}

	b.WriteString("Here are some review comments on the code currently in `git diff`. ")
	b.WriteString("If comments are questions, answer those questions. ")
	b.WriteString("If comments are unclear, or shouldn't be integrated, ask for feedback and confirmation. ")
	b.WriteString("Else integrate the comments.\n")

	// Group comments by file.
	type fileComment struct {
		file    string
		lineNum int
		code    string
		comment string
	}
	var grouped []fileComment
	for _, lineNum := range m.sortedCommentLines() {
		c := m.comments[lineNum]
		idx := c.seqLine - 1 // seqLine is 1-based, lineMeta is 0-based
		if idx >= 0 && idx < len(m.lineMeta) {
			lm := m.lineMeta[idx]
			grouped = append(grouped, fileComment{
				file:    lm.file,
				lineNum: lm.lineNum,
				code:    lm.code,
				comment: c.comment,
			})
		}
	}

	// Sort by file then line number.
	sort.Slice(grouped, func(i, j int) bool {
		if grouped[i].file != grouped[j].file {
			return grouped[i].file < grouped[j].file
		}
		return grouped[i].lineNum < grouped[j].lineNum
	})

	currentFile := ""
	for _, fc := range grouped {
		if fc.file != currentFile {
			currentFile = fc.file
			b.WriteString(fmt.Sprintf("\n## %s\n", currentFile))
		}
		if fc.lineNum > 0 {
			b.WriteString(fmt.Sprintf("\n### Line %d\n", fc.lineNum))
		} else {
			b.WriteString("\n### (header)\n")
		}
		if fc.code != "" {
			b.WriteString("```\n")
			b.WriteString(fc.code)
			b.WriteString("\n```\n")
		}
		b.WriteString("> " + strings.ReplaceAll(fc.comment, "\n", "\n> "))
		b.WriteString("\n")
	}

	return b.String()
}

// hasComments returns true if there are any review comments.
func (m diffModel) hasComments() bool {
	return len(m.comments) > 0
}

// sortedCommentLines returns comment line numbers sorted ascending.
func (m diffModel) sortedCommentLines() []int {
	lines := make([]int, 0, len(m.comments))
	for lineNum := range m.comments {
		lines = append(lines, lineNum)
	}
	sort.Ints(lines)
	return lines
}

// parseLogEntries extracts commit hashes from git log --oneline --graph output.
func parseLogEntries(gitLog string) []logEntry {
	lines := strings.Split(gitLog, "\n")
	entries := make([]logEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		hash := extractHash(line)
		entries = append(entries, logEntry{line: line, hash: hash})
	}
	return entries
}

// extractHash finds the commit hash in a git log --oneline --graph line.
// Lines look like: "* abc1234 msg", "| * abc1234 msg", "* abc1234 (HEAD -> main) msg"
func extractHash(line string) string {
	// Skip graph characters (*, |, /, \, space, _) to find the hash.
	i := 0
	for i < len(line) {
		c := line[i]
		if c == '*' || c == '|' || c == '/' || c == '\\' || c == ' ' || c == '_' {
			i++
		} else {
			break
		}
	}
	if i >= len(line) {
		return ""
	}
	// The hash is the next word.
	end := i
	for end < len(line) && line[end] != ' ' {
		end++
	}
	hash := line[i:end]
	// Validate it looks like a hex hash (at least 7 chars).
	if len(hash) >= 7 && isHex(hash) {
		return hash
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (m diffModel) Update(msg tea.Msg) (diffModel, tea.Cmd) {
	switch msg := msg.(type) {
	case diffLoadedMsg:
		m.loading = false
		m.diff = msg.diff
		m.err = msg.err
		if msg.diff != "" {
			m.lineMeta = parseDiffMeta(msg.diff)
		}

	case gitLogLoadedMsg:
		m.logLoading = false
		if msg.err == nil {
			m.gitLog = msg.log
			m.logEntries = parseLogEntries(msg.log)
			if m.logCursor >= len(m.logEntries) {
				m.logCursor = 0
			}
		}

	case gitInfoLoadedMsg:
		if msg.err == nil {
			m.branch = msg.branch
		}

	case gitShowLoadedMsg:
		m.commitLoading = false
		if msg.err != nil {
			m.commitErr2 = msg.err
		} else {
			m.commitDetail = msg.show
			m.commitHash = msg.hash
			m.commitScroll = 0
		}

	case diffCommitDoneMsg:
		m.committing = false
		if msg.err != nil {
			m.commitErr = msg.err
		} else {
			m.commitErr = nil
			m.mode = diffModeView
			m.commitMsg = ""
			// Reload diff after commit.
			m.loading = true
			m.logLoading = true
			return m, tea.Batch(m.loadDiff(), m.loadGitLog())
		}

	case diffPushDoneMsg:
		m.committing = false
		if msg.err != nil {
			m.commitErr = msg.err
		} else {
			m.commitErr = nil
		}

	case tea.KeyMsg:
		// Route to mode-specific handlers.
		switch m.mode {
		case diffModeCommitMsg:
			return m.updateCommitInput(msg)
		case diffModeLineInput:
			return m.updateLineInput(msg)
		case diffModeComment:
			return m.updateCommentInput(msg)
		case diffModeSubmitReview:
			return m.updateSubmitReview(msg)
		case diffModeConfirmQuit:
			return m.updateConfirmQuit(msg)
		}

		// Commit detail view.
		if m.tab == diffTabCommit {
			halfPage := m.height / 2
			if halfPage < 1 {
				halfPage = 10
			}
			switch msg.String() {
			case "esc":
				m.tab = m.prevTab
				m.commitDetail = ""
				m.commitHash = ""
				m.commitErr2 = nil
			case "up", "k":
				if m.commitScroll > 0 {
					m.commitScroll--
				}
			case "down", "j":
				m.commitScroll++
			case "pgup":
				m.commitScroll -= 10
				if m.commitScroll < 0 {
					m.commitScroll = 0
				}
			case "pgdown":
				m.commitScroll += 10
			case "ctrl+u":
				m.commitScroll -= halfPage
				if m.commitScroll < 0 {
					m.commitScroll = 0
				}
			case "ctrl+d":
				m.commitScroll += halfPage
			}
			return m, nil
		}

		halfPage := m.height / 2
		if halfPage < 1 {
			halfPage = 10
		}

		switch msg.String() {
		case "tab":
			if m.tab == diffTabDiff {
				m.tab = diffTabLog
			} else {
				m.tab = diffTabDiff
			}
		case "up", "k":
			if m.tab == diffTabLog {
				if m.logCursor > 0 {
					m.logCursor--
				}
			} else {
				if m.scroll > 0 {
					m.scroll--
				}
			}
		case "down", "j":
			if m.tab == diffTabLog {
				if m.logCursor < len(m.logEntries)-1 {
					m.logCursor++
				}
			} else {
				m.scroll++
			}
		case "pgup":
			if m.tab == diffTabLog {
				m.logCursor -= 10
				if m.logCursor < 0 {
					m.logCursor = 0
				}
			} else {
				m.scroll -= 10
				if m.scroll < 0 {
					m.scroll = 0
				}
			}
		case "pgdown":
			if m.tab == diffTabLog {
				m.logCursor += 10
				if m.logCursor >= len(m.logEntries) {
					m.logCursor = len(m.logEntries) - 1
				}
				if m.logCursor < 0 {
					m.logCursor = 0
				}
			} else {
				m.scroll += 10
			}
		case "ctrl+u":
			if m.tab == diffTabLog {
				m.logCursor -= halfPage
				if m.logCursor < 0 {
					m.logCursor = 0
				}
			} else {
				m.scroll -= halfPage
				if m.scroll < 0 {
					m.scroll = 0
				}
			}
		case "ctrl+d":
			if m.tab == diffTabLog {
				m.logCursor += halfPage
				if m.logCursor >= len(m.logEntries) {
					m.logCursor = len(m.logEntries) - 1
				}
				if m.logCursor < 0 {
					m.logCursor = 0
				}
			} else {
				m.scroll += halfPage
			}
		case "enter":
			if m.tab == diffTabLog && len(m.logEntries) > 0 && m.logCursor < len(m.logEntries) {
				entry := m.logEntries[m.logCursor]
				if entry.hash != "" {
					m.prevTab = m.tab
					m.tab = diffTabCommit
					m.commitLoading = true
					m.commitErr2 = nil
					m.commitDetail = ""
					m.commitHash = entry.hash
					m.commitScroll = 0
					return m, m.loadGitShow(entry.hash)
				}
			}
			// On diff tab with comments, enter starts submit review flow.
			if m.tab == diffTabDiff && m.hasComments() {
				m.mode = diffModeSubmitReview
				m.reviewPrompt.Reset()
			}
		case "r":
			// Start adding a review comment (diff tab only).
			if m.tab == diffTabDiff && m.diff != "" {
				m.mode = diffModeLineInput
				m.lineAction = "comment"
				m.lineInput.Reset()
			}
		case "d":
			// Start deleting a review comment (diff tab only).
			if m.tab == diffTabDiff && m.hasComments() {
				m.mode = diffModeLineInput
				m.lineAction = "delete"
				m.lineInput.Reset()
			}
		case "c":
			if m.tab == diffTabDiff && m.diff != "" {
				m.mode = diffModeCommitMsg
				m.pushAfter = false
				m.amendMode = false
				m.commitMsg = ""
				m.commitErr = nil
			}
		case "C":
			if m.tab == diffTabDiff && m.diff != "" {
				m.mode = diffModeCommitMsg
				m.pushAfter = true
				m.amendMode = false
				m.commitMsg = ""
				m.commitErr = nil
			}
		case "a":
			if m.tab == diffTabDiff {
				m.mode = diffModeCommitMsg
				m.pushAfter = false
				m.amendMode = true
				m.commitMsg = ""
				m.commitErr = nil
			}
		case "A":
			if m.tab == diffTabDiff {
				m.mode = diffModeCommitMsg
				m.pushAfter = true
				m.amendMode = true
				m.commitMsg = ""
				m.commitErr = nil
			}
		case "p":
			if !m.committing {
				m.committing = true
				m.commitErr = nil
				return m, m.doPush()
			}
		}
	}
	return m, nil
}

// updateLineInput handles key input when entering a line number for r/d.
func (m diffModel) updateLineInput(msg tea.KeyMsg) (diffModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = diffModeView
		m.lineInput.Reset()
		return m, nil
	}

	handled, submitted := m.lineInput.HandleKey(msg)
	if submitted {
		lineStr := strings.TrimSpace(m.lineInput.Value())
		lineNum, err := strconv.Atoi(lineStr)
		if err != nil || lineNum < 1 || lineNum > len(m.lineMeta) {
			// Invalid line number, stay in input mode.
			m.lineInput.Reset()
			return m, nil
		}

		if m.lineAction == "delete" {
			delete(m.comments, lineNum)
			m.mode = diffModeView
			m.lineInput.Reset()
		} else {
			// Open comment editor for this line.
			m.pendingLine = lineNum
			m.mode = diffModeComment
			m.commentInput.Reset()
			// If there's an existing comment, load it.
			if existing, ok := m.comments[lineNum]; ok {
				m.commentInput.SetValue(existing.comment)
			}
			m.lineInput.Reset()
		}
		return m, nil
	}
	if !handled {
		// Only allow digits in line input.
	}
	return m, nil
}

// updateCommentInput handles key input when editing a review comment.
func (m diffModel) updateCommentInput(msg tea.KeyMsg) (diffModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = diffModeView
		m.commentInput.Reset()
		return m, nil
	}

	handled, submitted := m.commentInput.HandleKey(msg)
	if submitted {
		comment := strings.TrimSpace(m.commentInput.Value())
		if comment != "" {
			m.comments[m.pendingLine] = &reviewComment{
				seqLine: m.pendingLine,
				comment: comment,
			}
		}
		m.mode = diffModeView
		m.commentInput.Reset()
		return m, nil
	}
	_ = handled
	return m, nil
}

// updateSubmitReview handles the submit review confirmation screen.
func (m diffModel) updateSubmitReview(msg tea.KeyMsg) (diffModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = diffModeView
		m.reviewPrompt.Reset()
		return m, nil
	}

	handled, submitted := m.reviewPrompt.HandleKey(msg)
	if submitted {
		prompt := m.buildReviewPrompt(m.reviewPrompt.Value())
		m.comments = make(map[int]*reviewComment)
		m.mode = diffModeView
		m.reviewPrompt.Reset()
		return m, func() tea.Msg {
			return diffReviewSubmitMsg{
				sessionID: m.sessionID,
				prompt:    prompt,
			}
		}
	}
	_ = handled
	return m, nil
}

// updateConfirmQuit handles the "discard comments?" confirmation.
func (m diffModel) updateConfirmQuit(msg tea.KeyMsg) (diffModel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		// Discard comments and signal quit.
		m.comments = make(map[int]*reviewComment)
		m.mode = diffModeView
		// Return a special value to signal the quit should proceed.
		// We set comments to empty so the outer q/esc handler will pass through.
		return m, nil
	case "n", "N", "esc":
		m.mode = diffModeView
		return m, nil
	}
	return m, nil
}

func (m diffModel) updateCommitInput(msg tea.KeyMsg) (diffModel, tea.Cmd) {
	if m.committing {
		return m, nil
	}
	switch msg.String() {
	case "enter":
		if strings.TrimSpace(m.commitMsg) != "" {
			m.committing = true
			return m, m.doCommit()
		}
	case "esc":
		m.mode = diffModeView
		m.commitMsg = ""
		m.commitErr = nil
	case "backspace":
		if len(m.commitMsg) > 0 {
			m.commitMsg = m.commitMsg[:len(m.commitMsg)-1]
		}
	case "ctrl+u":
		m.commitMsg = ""
	default:
		if msg.Type == tea.KeyRunes {
			m.commitMsg += string(msg.Runes)
		} else if msg.String() == " " {
			m.commitMsg += " "
		}
	}
	return m, nil
}

// shouldConfirmQuit returns true if quit should be intercepted for confirmation.
func (m *diffModel) shouldConfirmQuit() bool {
	if m.hasComments() && m.mode != diffModeConfirmQuit {
		m.mode = diffModeConfirmQuit
		return true
	}
	return false
}

func (m diffModel) View() string {
	var b strings.Builder

	// Title with branch name.
	title := " Git "
	if m.branch != "" {
		title += fmt.Sprintf("(%s) ", m.branch)
	}
	title += truncate(m.sessionID, 20) + " "
	b.WriteString(titleStyle.Render(title))

	// Tab bar.
	diffLabel := " Diff "
	logLabel := " Log "
	if m.tab == diffTabCommit {
		// In commit detail view, show commit hash as active tab.
		diffLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(diffLabel)
		logLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(logLabel)
		commitLabel := fmt.Sprintf(" %s ", truncate(m.commitHash, 10))
		commitLabel = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(commitLabel)
		b.WriteString("  " + diffLabel + " " + logLabel + " " + commitLabel)
	} else if m.tab == diffTabDiff {
		diffLabel = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(diffLabel)
		logLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(logLabel)
		b.WriteString("  " + diffLabel + " " + logLabel)
		// Show comment count if any.
		if m.hasComments() {
			count := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render(
				fmt.Sprintf(" [%d comment(s)]", len(m.comments)))
			b.WriteString(count)
		}
	} else {
		diffLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(diffLabel)
		logLabel = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(logLabel)
		b.WriteString("  " + diffLabel + " " + logLabel)
	}
	b.WriteString("\n")

	switch m.tab {
	case diffTabDiff:
		b.WriteString(m.viewDiff())
	case diffTabLog:
		b.WriteString(m.viewLog())
	case diffTabCommit:
		b.WriteString(m.viewCommit())
	}

	return b.String()
}

var (
	lineNumStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Width(5).Align(lipgloss.Right)
	commentMarker  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("*")
	noCommentSpace = " "
)

func (m diffModel) viewDiff() string {
	var b strings.Builder

	if m.loading {
		b.WriteString("\n  Loading diff...")
		return b.String()
	}
	if m.err != nil {
		b.WriteString(fmt.Sprintf("\n  Error: %v", m.err))
		return b.String()
	}
	if m.diff == "" {
		b.WriteString("\n  No changes (working tree clean)")
		return b.String()
	}

	// Reserve space for bottom area (input, comments list, etc.).
	bottomHeight := 0
	switch m.mode {
	case diffModeCommitMsg:
		bottomHeight = 3
		if m.commitErr != nil {
			bottomHeight = 4
		}
	case diffModeLineInput:
		bottomHeight = 2
	case diffModeComment:
		bottomHeight = 4
	case diffModeSubmitReview:
		bottomHeight = 6 + len(m.comments)
	case diffModeConfirmQuit:
		bottomHeight = 2
	}

	// Render diff with colors and line numbers.
	rawLines := strings.Split(m.diff, "\n")
	viewHeight := m.height - 5 - bottomHeight
	if viewHeight < 1 {
		viewHeight = 20
	}

	// Available width for diff content (after gutter: 5 linenum + 1 marker + 1 space).
	gutterWidth := 7
	contentWidth := m.width - gutterWidth
	if contentWidth < 20 {
		contentWidth = 20
	}

	// Build visual lines with wrapping. Each visual line tracks the original
	// line number (seqNum). Continuation lines have seqNum = 0.
	type visualLine struct {
		seqNum int    // 1-based original line number; 0 for continuation
		text   string // raw text for this segment
	}
	var vlines []visualLine
	for i, line := range rawLines {
		seqNum := i + 1
		if len(line) <= contentWidth {
			vlines = append(vlines, visualLine{seqNum: seqNum, text: line})
		} else {
			first := true
			for len(line) > 0 {
				chunk := line
				if len(chunk) > contentWidth {
					chunk = chunk[:contentWidth]
				}
				line = line[len(chunk):]
				sn := 0
				if first {
					sn = seqNum
					first = false
				}
				vlines = append(vlines, visualLine{seqNum: sn, text: chunk})
			}
		}
	}

	// Clamp scroll.
	maxScroll := len(vlines) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}

	end := m.scroll + viewHeight
	if end > len(vlines) {
		end = len(vlines)
	}

	blankGutter := strings.Repeat(" ", gutterWidth)

	for i := m.scroll; i < end; i++ {
		vl := vlines[i]
		if vl.seqNum > 0 {
			numStr := lineNumStyle.Render(strconv.Itoa(vl.seqNum))
			marker := noCommentSpace
			if _, hasComment := m.comments[vl.seqNum]; hasComment {
				marker = commentMarker
			}
			b.WriteString(numStr + marker + " " + colorDiffLine(vl.text) + "\n")
		} else {
			b.WriteString(blankGutter + colorDiffLine(vl.text) + "\n")
		}
	}

	// Status line.
	if len(vlines) > viewHeight {
		// Find original line range for status display.
		firstLine, lastLine := 0, 0
		for i := m.scroll; i < end; i++ {
			if vlines[i].seqNum > 0 {
				if firstLine == 0 {
					firstLine = vlines[i].seqNum
				}
				lastLine = vlines[i].seqNum
			}
		}
		statusParts := []string{fmt.Sprintf("line %d-%d of %d", firstLine, lastLine, len(rawLines))}
		if m.hasComments() {
			statusParts = append(statusParts, "r=comment d=delete Enter=submit")
		} else {
			statusParts = append(statusParts, "r=comment")
		}
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			"  " + strings.Join(statusParts, "  ")))
		b.WriteString("\n")
	}

	// Mode-specific bottom area.
	switch m.mode {
	case diffModeCommitMsg:
		b.WriteString(m.viewCommitMsgInput())
	case diffModeLineInput:
		b.WriteString(m.viewLineInput())
	case diffModeComment:
		b.WriteString(m.viewCommentInput())
	case diffModeSubmitReview:
		b.WriteString(m.viewSubmitReview())
	case diffModeConfirmQuit:
		b.WriteString(m.viewConfirmQuit())
	}

	return b.String()
}

func (m diffModel) viewLineInput() string {
	var b strings.Builder
	action := "Comment on line"
	if m.lineAction == "delete" {
		action = "Delete comment on line"
	}
	b.WriteString("\n  " + labelStyle.Render(action+":") + " " + fieldActiveStyle.Render(m.lineInput.Render()))
	return b.String()
}

func (m diffModel) viewCommentInput() string {
	var b strings.Builder
	lineNum := m.pendingLine
	label := fmt.Sprintf("Comment on line %d", lineNum)
	b.WriteString("\n  " + labelStyle.Render(label))
	// Show the code at that line (without width constraint).
	if lineNum > 0 && lineNum <= len(m.lineMeta) {
		lm := m.lineMeta[lineNum-1]
		if lm.code != "" {
			codePrev := lm.code
			maxWidth := m.width - 30
			if maxWidth < 40 {
				maxWidth = 40
			}
			if len(codePrev) > maxWidth {
				codePrev = codePrev[:maxWidth] + "..."
			}
			excerpt := fmt.Sprintf("%s:%d  %s", lm.file, lm.lineNum, strings.TrimSpace(codePrev))
			b.WriteString(" " + lipgloss.NewStyle().Foreground(colorMuted).Render(excerpt))
		}
	}
	inputWidth := m.width - 6 // account for "  " prefix and padding
	if inputWidth < 20 {
		inputWidth = 20
	}
	wrappedLines := m.commentInput.RenderWrapped(inputWidth, 2)
	for i, line := range wrappedLines {
		if i == 0 {
			b.WriteString("\n  " + fieldActiveStyle.Render(line))
		} else {
			b.WriteString("\n  " + fieldActiveStyle.Render(line))
		}
	}
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("Enter=save  Ctrl+J=newline  Esc=cancel"))
	return b.String()
}

func (m diffModel) viewSubmitReview() string {
	var b strings.Builder
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("Submit review comments?"))
	b.WriteString("\n")

	// Show comment summary (sorted by line number).
	for _, lineNum := range m.sortedCommentLines() {
		c := m.comments[lineNum]
		commentPreview := c.comment
		if len(commentPreview) > 50 {
			commentPreview = commentPreview[:50] + "..."
		}
		file := ""
		realLine := 0
		if lineNum > 0 && lineNum <= len(m.lineMeta) {
			lm := m.lineMeta[lineNum-1]
			file = lm.file
			realLine = lm.lineNum
		}
		loc := fmt.Sprintf("%s:%d", file, realLine)
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA")).Render(loc),
			lipgloss.NewStyle().Foreground(colorMuted).Render(commentPreview)))
	}

	b.WriteString("\n  " + labelStyle.Render("Overall comment (optional):") + " " + fieldActiveStyle.Render(m.reviewPrompt.Render()))
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("Enter=submit  Esc=cancel"))
	return b.String()
}

func (m diffModel) viewConfirmQuit() string {
	return fmt.Sprintf("\n  %s (%d comment(s) will be lost)  y/n",
		lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render("Discard review comments?"),
		len(m.comments))
}

func (m diffModel) viewCommitMsgInput() string {
	var b strings.Builder
	b.WriteString("\n")
	action := "Commit message"
	if m.amendMode && m.pushAfter {
		action = "Amend+force-push message"
	} else if m.amendMode {
		action = "Amend message"
	} else if m.pushAfter {
		action = "Commit+push message"
	}
	if m.committing {
		if m.amendMode && m.pushAfter {
			b.WriteString("  Amending and force-pushing...")
		} else if m.amendMode {
			b.WriteString("  Amending...")
		} else if m.pushAfter {
			b.WriteString("  Committing and pushing...")
		} else {
			b.WriteString("  Committing...")
		}
	} else {
		b.WriteString("  " + labelStyle.Render(action+":") + " " + fieldActiveStyle.Render(m.commitMsg+"█"))
	}
	if m.commitErr != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(m.commitErr.Error()))
	}
	return b.String()
}

func (m diffModel) viewLog() string {
	var b strings.Builder

	if m.logLoading {
		b.WriteString("\n  Loading git log...")
		return b.String()
	}
	if len(m.logEntries) == 0 {
		b.WriteString("\n  No commits")
		return b.String()
	}

	viewHeight := m.height - 5
	if viewHeight < 1 {
		viewHeight = 20
	}

	// Auto-scroll to keep cursor visible.
	if m.logCursor < m.logScroll {
		m.logScroll = m.logCursor
	}
	if m.logCursor >= m.logScroll+viewHeight {
		m.logScroll = m.logCursor - viewHeight + 1
	}

	maxScroll := len(m.logEntries) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.logScroll > maxScroll {
		m.logScroll = maxScroll
	}

	end := m.logScroll + viewHeight
	if end > len(m.logEntries) {
		end = len(m.logEntries)
	}

	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("#333333"))

	for i := m.logScroll; i < end; i++ {
		entry := m.logEntries[i]
		styled := colorLogLine(entry.line)
		if i == m.logCursor {
			// Render selected line with highlight background.
			styled = selectedStyle.Render(colorLogLineRaw(entry.line))
		}
		b.WriteString(styled)
		b.WriteString("\n")
	}

	if len(m.logEntries) > viewHeight {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  %d/%d  Enter=view commit", m.logCursor+1, len(m.logEntries))))
		b.WriteString("\n")
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			"  Enter=view commit"))
		b.WriteString("\n")
	}

	return b.String()
}

func (m diffModel) viewCommit() string {
	var b strings.Builder

	if m.commitLoading {
		b.WriteString("\n  Loading commit " + m.commitHash + "...")
		return b.String()
	}
	if m.commitErr2 != nil {
		b.WriteString(fmt.Sprintf("\n  Error: %v", m.commitErr2))
		b.WriteString("\n\n  Press Esc to go back")
		return b.String()
	}
	if m.commitDetail == "" {
		b.WriteString("\n  No commit data")
		b.WriteString("\n\n  Press Esc to go back")
		return b.String()
	}

	rawLines := strings.Split(m.commitDetail, "\n")
	viewHeight := m.height - 5
	if viewHeight < 1 {
		viewHeight = 20
	}

	// Wrap long lines to fit terminal width.
	commitContentWidth := m.width - 2
	if commitContentWidth < 20 {
		commitContentWidth = 20
	}
	type vline struct {
		text string
	}
	var vlines []vline
	for _, line := range rawLines {
		if len(line) <= commitContentWidth {
			vlines = append(vlines, vline{text: line})
		} else {
			for len(line) > 0 {
				chunk := line
				if len(chunk) > commitContentWidth {
					chunk = chunk[:commitContentWidth]
				}
				line = line[len(chunk):]
				vlines = append(vlines, vline{text: chunk})
			}
		}
	}

	maxScroll := len(vlines) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.commitScroll > maxScroll {
		m.commitScroll = maxScroll
	}

	end := m.commitScroll + viewHeight
	if end > len(vlines) {
		end = len(vlines)
	}

	for i := m.commitScroll; i < end; i++ {
		b.WriteString(colorDiffLine(vlines[i].text))
		b.WriteString("\n")
	}

	if len(vlines) > viewHeight {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  line %d-%d of %d  Esc=back", m.commitScroll+1, end, len(vlines))))
		b.WriteString("\n")
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			"  Esc=back"))
		b.WriteString("\n")
	}

	return b.String()
}

var (
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	diffRemoveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA"))
	diffHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	logGraphStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	logHashStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA"))
	logDecorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
)

func colorDiffLine(line string) string {
	if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
		return diffHeaderStyle.Render(line)
	}
	if strings.HasPrefix(line, "+") {
		return diffAddStyle.Render(line)
	}
	if strings.HasPrefix(line, "-") {
		return diffRemoveStyle.Render(line)
	}
	if strings.HasPrefix(line, "@@") {
		return diffHunkStyle.Render(line)
	}
	if strings.HasPrefix(line, "diff ") {
		return diffHeaderStyle.Render(line)
	}
	return line
}

func colorLogLine(line string) string {
	// Color the graph characters and hash in git log --oneline --graph output.
	// Lines look like: "* abc1234 Some commit message" or "| * abc1234 msg"
	trimmed := strings.TrimLeft(line, " ")

	// Find where graph chars end and hash begins.
	graphEnd := 0
	for i, c := range line {
		if c == '*' || c == '|' || c == '/' || c == '\\' || c == ' ' || c == '_' {
			graphEnd = i + 1
		} else {
			break
		}
	}

	if graphEnd > 0 && graphEnd < len(line) {
		graph := logGraphStyle.Render(line[:graphEnd])
		rest := line[graphEnd:]
		// Try to color the hash (first word after graph).
		if idx := strings.Index(rest, " "); idx > 0 {
			hash := logHashStyle.Render(rest[:idx])
			msg := rest[idx:]
			// Color decorations like (HEAD -> main, origin/main).
			if strings.Contains(msg, "(") {
				parts := strings.SplitN(msg, "(", 2)
				if len(parts) == 2 {
					closeParen := strings.Index(parts[1], ")")
					if closeParen >= 0 {
						decor := logDecorStyle.Render("(" + parts[1][:closeParen] + ")")
						msg = parts[0] + decor + parts[1][closeParen+1:]
					}
				}
			}
			return graph + hash + msg
		}
		return graph + rest
	}

	if len(trimmed) > 0 && trimmed[0] == '*' {
		return logGraphStyle.Render(line)
	}
	return line
}

// colorLogLineRaw colors a log line for the selected (highlighted) row.
// Uses plain text so the background highlight can show through.
func colorLogLineRaw(line string) string {
	// For the selected line, just return the raw text without ANSI colors
	// so the background highlight works cleanly.
	return line
}
