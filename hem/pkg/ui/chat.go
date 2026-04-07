package ui

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"james/hem/pkg/protocol"
)

type chatSessionStoppedMsg struct{ err error }
type chatSessionCompletedMsg struct{ err error }
type chatSessionDeletedMsg struct{ err error }
type chatSubagentDeletedMsg struct {
	sessionID string
	err       error
}
type chatOpenSubagentMsg struct {
	sessionID string
	name      string
}
type chatSubagentCreatedMsg struct {
	sessionID string
	name      string
	err       error
}
type chatPollTickMsg struct{}
type chatBroadcastMsg struct{ resp *protocol.Response }
type chatBroadcastReconnectMsg struct{}

const (
	chatPollInterval       = 180 * time.Second // Slow fallback (notifications enabled)
	chatPollIntervalIdle   = 30 * time.Second  // Idle poll (no notifications)
	chatPollIntervalActive = 3 * time.Second   // Fast poll when session is working (no notifications)
)

// chatModel displays conversation history and allows sending messages.
const chatPageSize = 10

type subagentInfo struct {
	SessionID string
	Name      string
	Status    string
	Yolo      bool
}

// fileComment is a comment on a specific line in the file viewer.
type fileComment struct {
	lineNum int
	comment string
}

// fileViewMode controls the file viewer's input state.
type fileViewMode int

const (
	fileViewModeView     fileViewMode = iota // browsing the file
	fileViewModeLineInput                     // entering a line number
	fileViewModeComment                       // typing a comment
	fileViewModeSubmit                        // submitting review
)

// fileReviewSubmitMsg is sent when the user submits file review comments.
type fileReviewSubmitMsg struct {
	prompt string
}

type chatModel struct {
	sessionID     string
	sessionName   string
	moneypennyName string
	sessionStatus string // moneypenny status (ready, working, etc.)
	conversation  []conversationTurn
	totalTurns    int // total turns on server
	recentCount   int // number of turns in the latest page (for comparison on refresh)
	schedules     []scheduleInfo
	subagents     []subagentInfo
	activity      []activityEvent // recent agent activity (thinking, tool calls)
	chatInput     textInput
	width         int
	height        int
	scroll        int // scroll offset from bottom
	err           error
	loading       bool
	loadingMore   bool // loading older messages
	polling       bool // a poll loadHistory is in-flight
	sending       bool
	commandMode      bool
	confirmDelete    bool
	pickingSubagent      bool // subagent picker overlay
	subagentCursor       int
	confirmDeleteSubagent bool // double-press delete confirmation in picker
	isSubagent       bool // true when viewing a subagent chat
	creatingSubagent bool   // entering prompt for new subagent
	subagentPrompt   string // prompt input for new subagent
	subagentPromptPos int   // cursor position in subagent prompt
	scheduling    bool   // in schedule prompt entry mode
	scheduleAt    string // time for the scheduled prompt
	pickingSchedule      bool // schedule picker overlay
	scheduleCursor       int
	confirmDeleteSchedule bool
	workingVerb   string // random spy verb chosen once per working session
	browsingFiles    bool       // file browser overlay
	browserPath      string     // current directory in browser
	browserEntries   []dirEntry // directory listing
	browserCursor    int
	browserLoading   bool
	browserErr       error
	// In-TUI file viewer (h key in browser)
	viewingFile         bool     // file viewer overlay active
	viewFileName        string   // name of file being viewed
	viewFileRemotePath  string   // remote path for review context
	viewFileLines       []string // file content lines
	viewFileScroll      int      // scroll offset in file viewer
	viewFileComments    map[int]*fileComment // keyed by line number (1-based)
	viewFileMode        fileViewMode         // current input mode
	viewFilePendingLine int                  // line being commented
	viewFileLineInput   textInput            // for line number entry
	viewFileCommentInput textInput           // for comment text
	viewFileReviewPrompt textInput           // for overall review comment
	// Download folder picker (d key in browser)
	downloadMode       bool       // download folder picker active
	downloadPath       string     // current directory in download picker
	downloadEntries    []dirEntry // directory listing for download picker
	downloadCursor     int
	downloadLoading    bool
	downloadErr        error
	downloadSourcePath string     // remote path of file to download
	client        *client
	renderCache      map[string]string // key: width+"\x00"+content → rendered markdown
}

// downloadDirs returns only directory entries from the download browser listing.
func (m chatModel) downloadDirs() []dirEntry {
	var dirs []dirEntry
	for _, e := range m.downloadEntries {
		if e.IsDir {
			dirs = append(dirs, e)
		}
	}
	return dirs
}

// File viewer input handlers.

func (m chatModel) updateFileViewBrowse(msg tea.KeyMsg) (chatModel, tea.Cmd) {
	viewHeight := m.height - 4
	if viewHeight < 1 {
		viewHeight = 20
	}
	switch msg.String() {
	case "esc", "q":
		m.viewingFile = false
		return m, nil
	case "up", "k":
		if m.viewFileScroll > 0 {
			m.viewFileScroll--
		}
	case "down", "j":
		maxScroll := len(m.viewFileLines) - viewHeight
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.viewFileScroll < maxScroll {
			m.viewFileScroll++
		}
	case "pgup":
		m.viewFileScroll -= viewHeight
		if m.viewFileScroll < 0 {
			m.viewFileScroll = 0
		}
	case "pgdown":
		maxScroll := len(m.viewFileLines) - viewHeight
		if maxScroll < 0 {
			maxScroll = 0
		}
		m.viewFileScroll += viewHeight
		if m.viewFileScroll > maxScroll {
			m.viewFileScroll = maxScroll
		}
	case "home":
		m.viewFileScroll = 0
	case "end", "G":
		maxScroll := len(m.viewFileLines) - viewHeight
		if maxScroll < 0 {
			maxScroll = 0
		}
		m.viewFileScroll = maxScroll
	case "r":
		// Add a review comment: enter line number.
		m.viewFileMode = fileViewModeLineInput
		m.viewFileLineInput.Reset()
	case "d":
		// Delete a comment: enter line number then remove.
		if len(m.viewFileComments) > 0 {
			m.viewFileMode = fileViewModeLineInput
			m.viewFileLineInput.Reset()
			m.viewFilePendingLine = -1 // signals delete mode
		}
	case "s":
		// Submit review comments.
		if len(m.viewFileComments) > 0 {
			m.viewFileMode = fileViewModeSubmit
			m.viewFileReviewPrompt.Reset()
		}
	}
	return m, nil
}

func (m chatModel) updateFileViewLineInput(msg tea.KeyMsg) (chatModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.viewFileMode = fileViewModeView
		m.viewFileLineInput.Reset()
		return m, nil
	}
	_, submitted := m.viewFileLineInput.HandleKey(msg)
	if submitted {
		lineStr := strings.TrimSpace(m.viewFileLineInput.Value())
		lineNum, err := strconv.Atoi(lineStr)
		if err != nil || lineNum < 1 || lineNum > len(m.viewFileLines) {
			m.viewFileLineInput.Reset()
			m.viewFileMode = fileViewModeView
			return m, nil
		}
		if m.viewFilePendingLine == -1 {
			// Delete mode.
			delete(m.viewFileComments, lineNum)
			m.viewFileMode = fileViewModeView
			m.viewFileLineInput.Reset()
		} else {
			// Comment mode: open comment editor.
			m.viewFilePendingLine = lineNum
			m.viewFileMode = fileViewModeComment
			m.viewFileCommentInput.Reset()
			if existing, ok := m.viewFileComments[lineNum]; ok {
				m.viewFileCommentInput.SetValue(existing.comment)
			}
		}
		return m, nil
	}
	return m, nil
}

func (m chatModel) updateFileViewComment(msg tea.KeyMsg) (chatModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.viewFileMode = fileViewModeView
		m.viewFileCommentInput.Reset()
		return m, nil
	}
	_, submitted := m.viewFileCommentInput.HandleKey(msg)
	if submitted {
		comment := strings.TrimSpace(m.viewFileCommentInput.Value())
		if comment != "" {
			m.viewFileComments[m.viewFilePendingLine] = &fileComment{
				lineNum: m.viewFilePendingLine,
				comment: comment,
			}
		}
		m.viewFileMode = fileViewModeView
		m.viewFileCommentInput.Reset()
		return m, nil
	}
	return m, nil
}

func (m chatModel) updateFileViewSubmit(msg tea.KeyMsg) (chatModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.viewFileMode = fileViewModeView
		m.viewFileReviewPrompt.Reset()
		return m, nil
	}
	_, submitted := m.viewFileReviewPrompt.HandleKey(msg)
	if submitted {
		prompt := m.buildFileReviewPrompt(m.viewFileReviewPrompt.Value())
		m.viewFileComments = make(map[int]*fileComment)
		m.viewFileMode = fileViewModeView
		m.viewFileReviewPrompt.Reset()
		return m, func() tea.Msg {
			return fileReviewSubmitMsg{prompt: prompt}
		}
	}
	return m, nil
}

func (m chatModel) sortedFileCommentLines() []int {
	lines := make([]int, 0, len(m.viewFileComments))
	for ln := range m.viewFileComments {
		lines = append(lines, ln)
	}
	sort.Ints(lines)
	return lines
}

func (m chatModel) buildFileReviewPrompt(overallComment string) string {
	var b strings.Builder

	if strings.TrimSpace(overallComment) != "" {
		b.WriteString(overallComment)
		b.WriteString("\n\n")
	}

	b.WriteString("Here are some review comments on the file `")
	b.WriteString(m.viewFileRemotePath)
	b.WriteString("`. ")
	b.WriteString("If comments are questions, answer those questions. ")
	b.WriteString("If comments are unclear, or shouldn't be integrated, ask for feedback and confirmation. ")
	b.WriteString("Else integrate the comments.\n")

	for _, lineNum := range m.sortedFileCommentLines() {
		c := m.viewFileComments[lineNum]
		b.WriteString(fmt.Sprintf("\n### Line %d\n", lineNum))
		// Include the code at that line for context.
		if lineNum > 0 && lineNum <= len(m.viewFileLines) {
			code := m.viewFileLines[lineNum-1]
			if strings.TrimSpace(code) != "" {
				b.WriteString("```\n")
				b.WriteString(code)
				b.WriteString("\n```\n")
			}
		}
		b.WriteString("> " + strings.ReplaceAll(c.comment, "\n", "\n> "))
		b.WriteString("\n")
	}

	return b.String()
}

func newChatModel(c *client, sessionID, sessionName, moneypennyName string) chatModel {
	return chatModel{
		client:         c,
		sessionID:      sessionID,
		sessionName:    sessionName,
		moneypennyName: moneypennyName,
		loading:        true,
		chatInput:      newTextInput(true),
		renderCache:    make(map[string]string),
	}
}

// Messages
type historyLoadedMsg struct {
	conversation []conversationTurn
	total        int
	status       string // session status from moneypenny
	err          error
}

type olderHistoryLoadedMsg struct {
	conversation []conversationTurn
	total        int
	err          error
}

type messageSentMsg struct {
	response string
	queued   bool
	err      error
}

type schedulesLoadedMsg struct {
	schedules []scheduleInfo
	err       error
}

type activityLoadedMsg struct {
	activity []activityEvent
	err      error
}

type scheduleCreatedMsg struct {
	err error
}
type scheduleCancelledMsg struct {
	scheduleID int64
	err        error
}

type subagentsLoadedMsg struct {
	subagents []subagentInfo
	err       error
}

type browserLoadedMsg struct {
	path    string
	entries []dirEntry
	err     error
}

type fileTransferredMsg struct {
	localPath string
	err       error
}

type fileContentLoadedMsg struct {
	name       string
	remotePath string // full remote path for review context
	content    string // decoded text content
	err        error
}

type fileDownloadedMsg struct {
	destPath string
	err      error
}

type downloadBrowserLoadedMsg struct {
	path    string
	entries []dirEntry
	err     error
}

func (m chatModel) loadHistory() tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		page, err := m.client.getHistoryPaginated(m.sessionID, chatPageSize, 0)
		if err != nil {
			uilog("loadHistory: error after %v: %v", time.Since(start), err)
			return historyLoadedMsg{err: err}
		}
		// Also fetch session status.
		var status string
		detail, err := m.client.showSession(m.sessionID)
		if err == nil {
			status = detail.Status
		}
		uilog("loadHistory: done in %v, turns=%d total=%d status=%s", time.Since(start), len(page.Conversation), page.Total, status)
		return historyLoadedMsg{conversation: page.Conversation, total: page.Total, status: status}
	}
}

func (m chatModel) loadOlderHistory() tea.Cmd {
	// We have the most recent len(conversation) turns.
	// "from" = offset from the end in the paginated query.
	// To get the next older page, skip what we already have.
	from := len(m.conversation)
	remaining := m.totalTurns - from
	if remaining <= 0 {
		return nil
	}
	count := chatPageSize
	if count > remaining {
		count = remaining
	}
	return func() tea.Msg {
		page, err := m.client.getHistoryPaginated(m.sessionID, count, from)
		if err != nil {
			return olderHistoryLoadedMsg{err: err}
		}
		return olderHistoryLoadedMsg{conversation: page.Conversation, total: page.Total}
	}
}

func (m chatModel) sendMessage(prompt string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.client.continueSession(m.sessionID, prompt)
		return messageSentMsg{response: result.Response, queued: result.Queued, err: err}
	}
}

func (m chatModel) loadSchedules() tea.Cmd {
	return func() tea.Msg {
		schedules, err := m.client.listSchedules(m.sessionID)
		return schedulesLoadedMsg{schedules: schedules, err: err}
	}
}

func (m chatModel) createSchedule(at, prompt string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.scheduleSession(m.sessionID, at, prompt)
		return scheduleCreatedMsg{err: err}
	}
}

// pendingSchedules returns schedules that are still pending.
func (m chatModel) pendingSchedules() []scheduleInfo {
	var result []scheduleInfo
	for _, s := range m.schedules {
		if s.Status == "pending" {
			result = append(result, s)
		}
	}
	return result
}

func (m chatModel) loadSubagents() tea.Cmd {
	return func() tea.Msg {
		subs, err := m.client.listSubagents(m.sessionID)
		if err != nil {
			return subagentsLoadedMsg{err: err}
		}
		var result []subagentInfo
		for _, s := range subs {
			result = append(result, subagentInfo{
				SessionID: s.SessionID,
				Name:      s.Name,
				Status:    s.Status,
				Yolo:      s.Yolo,
			})
		}
		return subagentsLoadedMsg{subagents: result}
	}
}

func (m chatModel) loadActivity() tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		events, err := m.client.getSessionActivity(m.sessionID)
		if err != nil {
			uilog("loadActivity: error after %v: %v", time.Since(start), err)
		} else {
			uilog("loadActivity: done in %v, events=%d", time.Since(start), len(events))
		}
		return activityLoadedMsg{activity: events, err: err}
	}
}

func (m chatModel) loadBrowser(path string) tea.Cmd {
	mp := m.moneypennyName
	return func() tea.Msg {
		entries, err := m.client.listDirectory(mp, path)
		if err != nil {
			return browserLoadedMsg{path: path, err: err}
		}
		return browserLoadedMsg{path: path, entries: entries}
	}
}

func (m chatModel) loadFileContent(remotePath string) tea.Cmd {
	mp := m.moneypennyName
	return func() tea.Msg {
		name, content, err := m.client.fetchFileContent(mp, remotePath)
		if err != nil {
			return fileContentLoadedMsg{err: err}
		}
		return fileContentLoadedMsg{name: name, remotePath: remotePath, content: content}
	}
}

func (m chatModel) downloadFile(remotePath, localDir string) tea.Cmd {
	mp := m.moneypennyName
	return func() tea.Msg {
		destPath, err := m.client.downloadFileTo(mp, remotePath, localDir)
		return fileDownloadedMsg{destPath: destPath, err: err}
	}
}

func loadDownloadBrowser(path string) tea.Cmd {
	return func() tea.Msg {
		entries, err := listLocalDir(path)
		return downloadBrowserLoadedMsg{path: path, entries: entries, err: err}
	}
}

func (m chatModel) transferAndOpen(remotePath string) tea.Cmd {
	mp := m.moneypennyName
	return func() tea.Msg {
		uilog("transferAndOpen: remotePath=%q mp=%q", remotePath, mp)
		localPath, err := m.client.transferFile(mp, remotePath)
		if err != nil {
			uilog("transferAndOpen: transfer error: %v", err)
			return fileTransferredMsg{err: err}
		}
		// Verify the file actually exists before trying to open it.
		if info, statErr := os.Stat(localPath); statErr != nil {
			uilog("transferAndOpen: file does not exist at %q: %v", localPath, statErr)
			return fileTransferredMsg{err: fmt.Errorf("transferred file missing: %w", statErr)}
		} else {
			uilog("transferAndOpen: file exists at %q, size=%d", localPath, info.Size())
		}
		// Open the file with the system default application.
		if err := openWithDefault(localPath); err != nil {
			uilog("transferAndOpen: open error: %v", err)
			return fileTransferredMsg{err: fmt.Errorf("opening %s: %w", localPath, err)}
		}
		uilog("transferAndOpen: opened successfully")
		return fileTransferredMsg{localPath: localPath}
	}
}

// openWithDefault opens a file with the OS default application.
// Detaches child stdin/stdout/stderr so bubbletea's raw terminal mode
// doesn't interfere with the launched application.
func openWithDefault(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	// Detach stdin from bubbletea's raw-mode terminal.
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		uilog("open command failed: %v output=%q", err, string(out))
		return fmt.Errorf("%w: %s", err, string(out))
	}
	uilog("open command succeeded for %q", path)
	return nil
}

// copyToClipboard copies text to the system clipboard.
func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		cmd = exec.Command("xclip", "-selection", "clipboard")
	case "windows":
		cmd = exec.Command("clip")
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// chatPollTickAdaptive returns a poll tick with an interval that depends on
// whether notifications are enabled and whether the session is working.
//
// With notifications (--ff-use-notifications): 180s fallback, broadcasts handle real-time.
// Without notifications (default): 3s when working, 30s when idle.
func (m chatModel) chatPollTickAdaptive() tea.Cmd {
	var interval time.Duration
	if m.client.useNotifications {
		interval = chatPollInterval
	} else if m.sessionStatus == "working" {
		interval = chatPollIntervalActive
	} else {
		interval = chatPollIntervalIdle
	}
	uilog("poll schedule: interval=%v status=%s notifications=%v", interval, m.sessionStatus, m.client.useNotifications)
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return chatPollTickMsg{}
	})
}

func listenForChatBroadcasts(broadcasts <-chan *protocol.Response, sessionID string) tea.Cmd {
	return func() tea.Msg {
		if broadcasts == nil {
			return chatBroadcastReconnectMsg{}
		}
		for {
			resp, ok := <-broadcasts
			if !ok {
				// Channel closed — MI6 connection lost.
				return chatBroadcastReconnectMsg{}
			}
			// Only process broadcasts for this session
			if matchesChatSession(resp, sessionID) {
				return chatBroadcastMsg{resp: resp}
			}
		}
	}
}

func matchesChatSession(resp *protocol.Response, sessionID string) bool {
	// Extract session_id from notification data
	var data map[string]interface{}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return false
	}
	sid, _ := data["session_id"].(string)
	return sid == sessionID
}

func (m chatModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.loadHistory(),
		m.loadSchedules(),
		m.loadSubagents(),
		m.loadActivity(),
		m.chatPollTickAdaptive(),
	}

	// Start broadcast listener
	if broadcasts := m.client.broadcasts(); broadcasts != nil {
		cmds = append(cmds, listenForChatBroadcasts(broadcasts, m.sessionID))
	}

	return tea.Batch(cmds...)
}

func (m chatModel) Update(msg tea.Msg) (chatModel, tea.Cmd) {
	switch msg := msg.(type) {
	case chatPollTickMsg:
		// Periodically reload history and schedules to catch agent responses.
		// Skip if a previous poll load is still in-flight to avoid races.
		uilog("poll tick: status=%s sending=%v loading=%v polling=%v", m.sessionStatus, m.sending, m.loading, m.polling)
		if !m.sending && !m.loading && !m.polling {
			m.polling = true
			cmds := []tea.Cmd{m.loadHistory(), m.loadSchedules(), m.loadSubagents(), m.loadActivity(), m.chatPollTickAdaptive()}
			return m, tea.Batch(cmds...)
		}
		return m, m.chatPollTickAdaptive()

	case chatBroadcastReconnectMsg:
		// MI6 broadcast channel closed — try to re-subscribe.
		if ch := m.client.broadcasts(); ch != nil {
			uilog("MI6 chat broadcast: reconnected, re-subscribing")
			return m, listenForChatBroadcasts(ch, m.sessionID)
		}
		uilog("MI6 chat broadcast: still disconnected, retrying in 3s")
		return m, func() tea.Msg {
			time.Sleep(3 * time.Second)
			return chatBroadcastReconnectMsg{}
		}

	case chatBroadcastMsg:
		// Handle real-time notifications from moneypenny
		uilog("broadcast: verb=%s noun=%s", msg.resp.Verb, msg.resp.Noun)
		cmds := []tea.Cmd{listenForChatBroadcasts(m.client.broadcasts(), m.sessionID)}

		// Route by event type (verb/noun from moneypenny notification)
		switch msg.resp.Verb {
		case "activity":
			// Real-time activity stream update during agent execution
			if !m.loading && !m.polling {
				cmds = append(cmds, m.loadActivity())
			}

		case "message":
			// New conversation message added
			if !m.loading && !m.polling {
				cmds = append(cmds, m.loadHistory())
			}

		case "status":
			// Session status changed (working/idle)
			if !m.loading && !m.polling {
				cmds = append(cmds, m.loadHistory())
			}

		case "subagent":
			// Subagent created or changed
			if !m.loading && !m.polling {
				cmds = append(cmds, m.loadSubagents())
			}

		case "schedule":
			// Schedule created/executed/deleted
			if !m.loading && !m.polling {
				cmds = append(cmds, m.loadSchedules())
			}
		}

		return m, tea.Batch(cmds...)

	case historyLoadedMsg:
		m.loading = false
		m.polling = false
		if msg.err == nil {
			uilog("history loaded: status=%s turns=%d total=%d", msg.status, len(msg.conversation), msg.total)
			m.sessionStatus = msg.status
			if msg.status == "working" && m.workingVerb == "" {
				m.workingVerb = pickSpyVerb()
			} else if msg.status != "working" {
				m.workingVerb = ""
			}

			// Don't replace existing conversation with empty data (race during working state).
			if len(msg.conversation) == 0 && len(m.conversation) > 0 {
				m.totalTurns = msg.total
				return m, nil
			}

			m.totalTurns = msg.total

			// Compare with the recent portion of our conversation.
			oldRecent := m.recentTurns()
			changed := len(msg.conversation) != len(oldRecent)
			if !changed {
				for i := range msg.conversation {
					if msg.conversation[i].Role != oldRecent[i].Role || msg.conversation[i].Content != oldRecent[i].Content {
						changed = true
						break
					}
				}
			}

			if changed {
				uilog("history changed: old=%d new=%d", len(oldRecent), len(msg.conversation))
				// Collect queued turns not yet in server data.
				var pendingQueued []conversationTurn
				for i := range m.conversation {
					if m.conversation[i].Role == "user" && m.conversation[i].Queued {
						found := false
						for j := range msg.conversation {
							if msg.conversation[j].Role == "user" && msg.conversation[j].Content == m.conversation[i].Content {
								found = true
								// Mark as queued if no response yet.
								hasResponse := j+1 < len(msg.conversation) && msg.conversation[j+1].Role == "assistant"
								if !hasResponse {
									msg.conversation[j].Queued = true
								}
								break
							}
						}
						if !found {
							pendingQueued = append(pendingQueued, m.conversation[i])
						}
					}
				}

				// Replace only the recent portion, keeping any older loaded history.
				olderCount := len(m.conversation) - m.recentCount
				if olderCount > 0 {
					m.conversation = append(m.conversation[:olderCount], msg.conversation...)
				} else {
					m.conversation = msg.conversation
				}

				// Re-append queued turns that the server doesn't know about yet.
				m.conversation = append(m.conversation, pendingQueued...)
				m.recentCount = len(msg.conversation) + len(pendingQueued)
				m.scroll = 0
			}
		} else if msg.err != nil {
			uilog("history load error: %v", msg.err)
		}
		m.err = msg.err

	case olderHistoryLoadedMsg:
		m.loadingMore = false
		if msg.err == nil && len(msg.conversation) > 0 {
			m.totalTurns = msg.total
			// Prepend older turns to the conversation.
			m.conversation = append(msg.conversation, m.conversation...)
		}

	case messageSentMsg:
		m.sending = false
		uilog("message sent: err=%v queued=%v", msg.err, msg.queued)
		if msg.err != nil {
			m.err = msg.err
		} else if msg.queued {
			// Prompt was queued — mark the last user turn as queued.
			if len(m.conversation) > 0 {
				last := &m.conversation[len(m.conversation)-1]
				if last.Role == "user" {
					last.Queued = true
				}
			}
		} else {
			// Sent successfully — optimistically show working status
			// until the next poll confirms the real state.
			m.sessionStatus = "working"
			m.workingVerb = pickSpyVerb()
		}
		// Start polling immediately so the working indicator and eventual
		// response are picked up without waiting for the next tick.
		return m, tea.Batch(m.loadHistory(), m.chatPollTickAdaptive())

	case schedulesLoadedMsg:
		if msg.err == nil {
			m.schedules = msg.schedules
		}

	case scheduleCreatedMsg:
		m.scheduling = false
		m.scheduleAt = ""
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadSchedules()

	case scheduleCancelledMsg:
		m.confirmDeleteSchedule = false
		if msg.err != nil {
			m.err = msg.err
		}
		// Remove the cancelled schedule from the list.
		for i, s := range m.schedules {
			if s.ID == msg.scheduleID {
				m.schedules = append(m.schedules[:i], m.schedules[i+1:]...)
				break
			}
		}
		// Adjust cursor.
		pending := m.pendingSchedules()
		if m.scheduleCursor >= len(pending) {
			m.scheduleCursor = max(0, len(pending)-1)
		}
		// If no more pending schedules, close picker.
		if len(pending) == 0 {
			m.pickingSchedule = false
		}
		return m, m.loadSchedules()

	case subagentsLoadedMsg:
		if msg.err == nil {
			m.subagents = msg.subagents
		}

	case activityLoadedMsg:
		uilog("activityLoaded: err=%v events=%d status=%s", msg.err, len(msg.activity), m.sessionStatus)
		if msg.err == nil {
			// Don't replace existing activity with empty while working — avoids flicker.
			if len(msg.activity) > 0 || m.sessionStatus != "working" {
				m.activity = msg.activity
			}
		}

	case chatSessionStoppedMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.commandMode = false
		}

	case browserLoadedMsg:
		m.browserLoading = false
		if msg.err != nil {
			m.browserErr = msg.err
		} else {
			m.browserPath = msg.path
			m.browserEntries = msg.entries
			m.browserCursor = 0
			m.browserErr = nil
		}

	case fileTransferredMsg:
		uilog("fileTransferred: localPath=%q err=%v", msg.localPath, msg.err)
		m.browsingFiles = false
		m.browserLoading = false
		if msg.err != nil {
			m.err = msg.err
		}

	case fileContentLoadedMsg:
		m.browserLoading = false
		if msg.err != nil {
			m.browserErr = msg.err
		} else {
			m.browsingFiles = false
			m.viewingFile = true
			m.viewFileName = msg.name
			m.viewFileRemotePath = msg.remotePath
			m.viewFileLines = strings.Split(msg.content, "\n")
			m.viewFileScroll = 0
			m.viewFileComments = make(map[int]*fileComment)
			m.viewFileMode = fileViewModeView
			m.viewFileLineInput = newTextInput(false)
			m.viewFileCommentInput = newTextInput(true)
			m.viewFileReviewPrompt = newTextInput(true)
		}

	case fileDownloadedMsg:
		m.downloadMode = false
		m.downloadLoading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.browsingFiles = false
			m.err = nil
			// Show success as a transient status (will be cleared on next action).
			m.browserErr = fmt.Errorf("Downloaded to %s", msg.destPath)
		}

	case downloadBrowserLoadedMsg:
		m.downloadLoading = false
		if msg.err != nil {
			m.downloadErr = msg.err
		} else {
			m.downloadPath = msg.path
			m.downloadEntries = msg.entries
			m.downloadCursor = 0
			m.downloadErr = nil
		}

	case chatSubagentCreatedMsg:
		m.creatingSubagent = false
		m.subagentPrompt = ""
		m.subagentPromptPos = 0
		if msg.err != nil {
			m.err = msg.err
		} else {
			// Open the newly created subagent.
			return m, func() tea.Msg {
				return chatOpenSubagentMsg{sessionID: msg.sessionID, name: msg.name}
			}
		}

	case tea.KeyMsg:
		if m.creatingSubagent {
			switch msg.String() {
			case "esc":
				m.creatingSubagent = false
				m.subagentPrompt = ""
				m.subagentPromptPos = 0
				return m, nil
			case "enter":
				prompt := strings.TrimSpace(m.subagentPrompt)
				if prompt != "" {
					sid := m.sessionID
					return m, func() tea.Msg {
						id, name, err := m.client.createSubagent(sid, prompt)
						return chatSubagentCreatedMsg{sessionID: id, name: name, err: err}
					}
				}
			case "backspace":
				if m.subagentPromptPos > 0 {
					_, size := utf8.DecodeLastRuneInString(m.subagentPrompt[:m.subagentPromptPos])
					m.subagentPrompt = m.subagentPrompt[:m.subagentPromptPos-size] + m.subagentPrompt[m.subagentPromptPos:]
					m.subagentPromptPos -= size
				}
			case "left":
				if m.subagentPromptPos > 0 {
					_, size := utf8.DecodeLastRuneInString(m.subagentPrompt[:m.subagentPromptPos])
					m.subagentPromptPos -= size
				}
			case "right":
				if m.subagentPromptPos < len(m.subagentPrompt) {
					_, size := utf8.DecodeRuneInString(m.subagentPrompt[m.subagentPromptPos:])
					m.subagentPromptPos += size
				}
			case "ctrl+r":
				m.subagentPrompt = ""
				m.subagentPromptPos = 0
			default:
				if msg.Type == tea.KeyRunes {
					s := string(msg.Runes)
					m.subagentPrompt = m.subagentPrompt[:m.subagentPromptPos] + s + m.subagentPrompt[m.subagentPromptPos:]
					m.subagentPromptPos += len(s)
				} else if msg.Type == tea.KeySpace {
					m.subagentPrompt = m.subagentPrompt[:m.subagentPromptPos] + " " + m.subagentPrompt[m.subagentPromptPos:]
					m.subagentPromptPos++
				}
			}
			return m, nil
		}

		if m.pickingSubagent {
			// Total items = subagents + 1 "New subagent..." entry
			totalItems := len(m.subagents) + 1
			// Any key other than "d" cancels the delete confirmation.
			if msg.String() != "d" {
				m.confirmDeleteSubagent = false
			}
			switch msg.String() {
			case "esc":
				m.pickingSubagent = false
				m.confirmDeleteSubagent = false
				return m, nil
			case "up", "k":
				if m.subagentCursor > 0 {
					m.subagentCursor--
				}
			case "down", "j":
				if m.subagentCursor < totalItems-1 {
					m.subagentCursor++
				}
			case "d":
				// Only allow delete on actual subagents, not "New subagent..." entry.
				if m.subagentCursor < len(m.subagents) {
					if !m.confirmDeleteSubagent {
						m.confirmDeleteSubagent = true
						return m, nil
					}
					// Confirmed — delete the subagent.
					m.confirmDeleteSubagent = false
					sub := m.subagents[m.subagentCursor]
					return m, func() tea.Msg {
						err := m.client.deleteSession(sub.SessionID)
						return chatSubagentDeletedMsg{sessionID: sub.SessionID, err: err}
					}
				}
			case "enter":
				if m.subagentCursor < len(m.subagents) {
					sub := m.subagents[m.subagentCursor]
					m.pickingSubagent = false
					m.confirmDeleteSubagent = false
					m.commandMode = false
					return m, func() tea.Msg {
						return chatOpenSubagentMsg{sessionID: sub.SessionID, name: sub.Name}
					}
				}
				// Last entry: "New subagent..."
				m.pickingSubagent = false
				m.confirmDeleteSubagent = false
				m.creatingSubagent = true
				m.subagentPrompt = ""
				m.subagentPromptPos = 0
				return m, nil
			}
			return m, nil
		}

		if m.pickingSchedule {
			pending := m.pendingSchedules()
			totalItems := len(pending) + 1 // +1 for "New schedule..." entry
			if msg.String() != "d" {
				m.confirmDeleteSchedule = false
			}
			switch msg.String() {
			case "esc":
				m.pickingSchedule = false
				m.confirmDeleteSchedule = false
				return m, nil
			case "up", "k":
				if m.scheduleCursor > 0 {
					m.scheduleCursor--
				}
			case "down", "j":
				if m.scheduleCursor < totalItems-1 {
					m.scheduleCursor++
				}
			case "d":
				if m.scheduleCursor < len(pending) {
					if !m.confirmDeleteSchedule {
						m.confirmDeleteSchedule = true
						return m, nil
					}
					// Confirmed — cancel the schedule.
					m.confirmDeleteSchedule = false
					sch := pending[m.scheduleCursor]
					return m, func() tea.Msg {
						err := m.client.cancelSchedule(m.sessionID, sch.ID)
						return scheduleCancelledMsg{scheduleID: sch.ID, err: err}
					}
				}
			case "enter":
				// Last entry: "New schedule..."
				if m.scheduleCursor >= len(pending) {
					m.pickingSchedule = false
					m.confirmDeleteSchedule = false
					m.commandMode = false
					m.scheduling = true
					m.scheduleAt = ""
					m.chatInput.Reset()
					return m, nil
				}
			}
			return m, nil
		}

		if m.browsingFiles {
			// Total items = ".." (if not root) + entries
			hasParent := m.browserPath != "/" && m.browserPath != ""
			totalItems := len(m.browserEntries)
			if hasParent {
				totalItems++
			}

			// Helper to get the selected entry (adjusting for ".." row).
			selectedEntry := func() *dirEntry {
				if m.browserLoading {
					return nil
				}
				idx := m.browserCursor
				if hasParent {
					if idx == 0 {
						return nil // ".." selected
					}
					idx--
				}
				if idx < len(m.browserEntries) {
					return &m.browserEntries[idx]
				}
				return nil
			}

			switch msg.String() {
			case "esc":
				m.browsingFiles = false
				m.browserErr = nil
				return m, nil
			case "up", "k":
				if m.browserCursor > 0 {
					m.browserCursor--
				}
			case "down", "j":
				if m.browserCursor < totalItems-1 {
					m.browserCursor++
				}
			case "enter":
				if m.browserLoading {
					return m, nil
				}
				// ".." selected — go up.
				if hasParent && m.browserCursor == 0 {
					m.browserLoading = true
					return m, m.loadBrowser(filepath.Dir(m.browserPath))
				}
				entry := selectedEntry()
				if entry != nil {
					if entry.IsDir {
						newPath := m.browserPath + "/" + entry.Name
						m.browserLoading = true
						return m, m.loadBrowser(newPath)
					}
					// File selected — transfer and open in default app.
					remotePath := m.browserPath + "/" + entry.Name
					m.browserLoading = true
					return m, m.transferAndOpen(remotePath)
				}
			case "h":
				// Open file in Hem (in-TUI viewer).
				entry := selectedEntry()
				if entry != nil && !entry.IsDir {
					remotePath := m.browserPath + "/" + entry.Name
					m.browserLoading = true
					return m, m.loadFileContent(remotePath)
				}
			case "d":
				// Download file to local directory.
				entry := selectedEntry()
				if entry != nil && !entry.IsDir {
					m.downloadSourcePath = m.browserPath + "/" + entry.Name
					m.downloadMode = true
					m.downloadLoading = true
					// Start from last used download path, or home dir.
					startPath, _ := m.client.getDefault("download-path")
					if startPath == "" {
						startPath, _ = os.UserHomeDir()
					}
					return m, loadDownloadBrowser(startPath)
				}
			case "c":
				// Copy full path to clipboard.
				entry := selectedEntry()
				if entry != nil {
					fullPath := m.browserPath + "/" + entry.Name
					if err := copyToClipboard(fullPath); err != nil {
						m.browserErr = fmt.Errorf("clipboard: %v", err)
					} else {
						m.browserErr = fmt.Errorf("Copied: %s", fullPath)
					}
				} else if m.browserPath != "" {
					// No entry selected (e.g. on ".."), copy current directory.
					if err := copyToClipboard(m.browserPath); err != nil {
						m.browserErr = fmt.Errorf("clipboard: %v", err)
					} else {
						m.browserErr = fmt.Errorf("Copied: %s", m.browserPath)
					}
				}
			}
			return m, nil
		}

		// Download folder picker overlay.
		if m.downloadMode {
			hasParent := m.downloadPath != "/" && m.downloadPath != ""
			totalItems := len(m.downloadEntries)
			if hasParent {
				totalItems++
			}
			// Only show directories in download picker.
			switch msg.String() {
			case "esc":
				m.downloadMode = false
				m.downloadErr = nil
				return m, nil
			case "up", "k":
				if m.downloadCursor > 0 {
					m.downloadCursor--
				}
			case "down", "j":
				if m.downloadCursor < totalItems-1 {
					m.downloadCursor++
				}
			case "enter":
				if m.downloadLoading {
					return m, nil
				}
				// Navigate into selected directory.
				if hasParent && m.downloadCursor == 0 {
					m.downloadLoading = true
					return m, loadDownloadBrowser(filepath.Dir(m.downloadPath))
				}
				idx := m.downloadCursor
				if hasParent {
					idx--
				}
				// Only directories in the list.
				dirs := m.downloadDirs()
				if idx < len(dirs) {
					newPath := m.downloadPath + "/" + dirs[idx].Name
					m.downloadLoading = true
					return m, loadDownloadBrowser(newPath)
				}
			case " ":
				// Space = confirm this directory as download destination.
				if m.downloadLoading {
					return m, nil
				}
				m.downloadLoading = true
				// Remember this path for next time.
				_ = m.client.setDefault("download-path", m.downloadPath)
				return m, m.downloadFile(m.downloadSourcePath, m.downloadPath)
			}
			return m, nil
		}

		// In-TUI file viewer overlay.
		if m.viewingFile {
			switch m.viewFileMode {
			case fileViewModeLineInput:
				return m.updateFileViewLineInput(msg)
			case fileViewModeComment:
				return m.updateFileViewComment(msg)
			case fileViewModeSubmit:
				return m.updateFileViewSubmit(msg)
			default:
				return m.updateFileViewBrowse(msg)
			}
		}

		if m.scheduling {
			switch msg.String() {
			case "esc":
				m.scheduling = false
				m.scheduleAt = ""
				m.chatInput.Reset()
				return m, nil
			case "enter":
				if m.scheduleAt == "" {
					// First enter: capture the time.
					at := strings.TrimSpace(m.chatInput.Value())
					if at == "" {
						return m, nil
					}
					m.scheduleAt = at
					m.chatInput.Reset()
					return m, nil
				}
				// Second enter: capture the prompt and create schedule.
				prompt := strings.TrimSpace(m.chatInput.Value())
				if prompt == "" {
					return m, nil
				}
				m.chatInput.Reset()
				return m, m.createSchedule(m.scheduleAt, prompt)
			}
			// Fall through to normal input handling for text entry.
		}

		if m.commandMode {
			// Most command mode keys are handled by updateChat in ui.go.
			// Chat model only handles enter (resume) and scroll.
			switch msg.String() {
			case "enter":
				m.commandMode = false
				m.confirmDelete = false
			case "pgup", "ctrl+u":
				m.scroll += 10
				if m.scroll > 0 && !m.loadingMore && len(m.conversation) < m.totalTurns {
					m.loadingMore = true
					return m, m.loadOlderHistory()
				}
			case "pgdown", "ctrl+d":
				m.scroll -= 10
				if m.scroll < 0 {
					m.scroll = 0
				}
			}
			return m, nil
		}

		// Handle scroll keys before delegating to textInput.
		switch msg.String() {
		case "pgup", "ctrl+u":
			m.scroll += 10
			if m.scroll > 0 && !m.loadingMore && len(m.conversation) < m.totalTurns {
				m.loadingMore = true
				return m, m.loadOlderHistory()
			}
			return m, nil
		case "pgdown", "ctrl+d":
			m.scroll -= 10
			if m.scroll < 0 {
				m.scroll = 0
			}
			return m, nil
		}

		// Delegate to textInput for all text editing.
		handled, submitted := m.chatInput.HandleKey(msg)
		if submitted {
			if m.scheduling {
				return m, nil // handled above
			}
			prompt := strings.TrimSpace(m.chatInput.Value())
			if prompt != "" && !m.sending {
				m.chatInput.Reset()
				m.sending = true
				m.err = nil
				m.conversation = append(m.conversation, conversationTurn{
					Role:    "user",
					Content: prompt,
				})
				m.recentCount++
				m.totalTurns++
				m.scroll = 0
				return m, m.sendMessage(prompt)
			}
		}
		_ = handled
	}
	return m, nil
}

func (m chatModel) View() string {
	var b strings.Builder

	// Title bar
	name := m.sessionName
	if name == "" {
		name = truncate(m.sessionID, 20)
	}
	var title string
	if m.isSubagent {
		title = fmt.Sprintf(" Subagent: %s ", name)
	} else {
		title = fmt.Sprintf(" Chat: %s ", name)
	}
	if m.moneypennyName != "" {
		title = fmt.Sprintf("%s(%s) ", title, m.moneypennyName)
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	if m.loading {
		b.WriteString("\n  Loading conversation...\n")
		return b.String()
	}

	// Calculate input area height for layout.
	inputWidth := m.width - 4
	if inputWidth < 20 {
		inputWidth = 80
	}
	inputLineCount := 1
	if !m.commandMode {
		inputLines := m.chatInput.RenderWrapped(inputWidth, 3)
		inputLineCount = len(inputLines)
		if m.sending {
			inputLineCount = 1
		}
	}

	// Calculate available height for messages.
	// Layout: title (1 line) + messages (msgHeight) + error (0 or 1) + input (inputLineCount)
	errLines := 0
	if m.err != nil {
		errLines = 1
	}
	msgHeight := m.height - 1 - errLines - inputLineCount

	// Render messages.
	var msgLines []string

	// Show indicator if there are older messages not yet loaded.
	if len(m.conversation) < m.totalTurns {
		remaining := m.totalTurns - len(m.conversation)
		if m.loadingMore {
			msgLines = append(msgLines, lipgloss.NewStyle().Foreground(colorMuted).Render(
				"  Loading older messages..."))
		} else {
			msgLines = append(msgLines, lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf("  ↑ %d older messages — scroll up to load", remaining)))
		}
		msgLines = append(msgLines, "")
	}

	systemMsgStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	for _, turn := range m.conversation {
		var prefix string
		switch turn.Role {
		case "user":
			if turn.Queued {
				prefix = userMsgStyle.Render("⏳ you") + " " + lipgloss.NewStyle().Foreground(colorMuted).Render("[Queued]")
			} else {
				prefix = userMsgStyle.Render("🧑‍💻 you")
			}
		case "system":
			prefix = systemMsgStyle.Render("⚙ system")
		default:
			agentLabel := m.sessionName
			if agentLabel == "" {
				agentLabel = "agent"
			}
			prefix = assistantMsgStyle.Render("🕴️ " + agentLabel)
		}
		if turn.CreatedAt != "" {
			prefix += " " + lipgloss.NewStyle().Foreground(colorMuted).Render(localTime(turn.CreatedAt))
		}
		msgLines = append(msgLines, prefix)

		// Render content (with caching for markdown).
		contentWidth := m.width - 4
		if contentWidth < 20 {
			contentWidth = 80
		}
		content := turn.Content
		if strings.TrimSpace(content) == "" {
			content = "(empty)"
		}
		var rendered string
		switch turn.Role {
		case "assistant", "user":
			rendered = m.cachedRenderMarkdown(content, contentWidth)
		case "system":
			rendered = wordWrap(content, contentWidth)
		default:
			rendered = wordWrap(content, contentWidth)
		}
		for _, line := range strings.Split(rendered, "\n") {
			switch turn.Role {
			case "assistant", "user":
				msgLines = append(msgLines, "  "+line)
			case "system":
				msgLines = append(msgLines, "  "+systemMsgStyle.Render(line))
			default:
				msgLines = append(msgLines, "  "+msgContentStyle.Render(line))
			}
		}
		msgLines = append(msgLines, "")
	}

	if m.sending || m.sessionStatus == "working" {
		if len(m.activity) > 0 {
			// Show recent activity events.
			activityStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
			// Show last few activity events.
			start := 0
			if len(m.activity) > 5 {
				start = len(m.activity) - 5
			}
			activityWidth := m.width - 8 // account for "  {icon} " prefix
			if activityWidth < 20 {
				activityWidth = 60
			}
			for _, ev := range m.activity[start:] {
				icon := "💭"
				if ev.Type == "tool_use" {
					icon = "🔧"
				} else if ev.Type == "text" {
					icon = "📝"
				}
				wrapped := wordWrap(ev.Summary, activityWidth)
				for i, line := range strings.Split(wrapped, "\n") {
					if i == 0 {
						msgLines = append(msgLines, activityStyle.Render(fmt.Sprintf("  %s %s", icon, line)))
					} else {
						msgLines = append(msgLines, activityStyle.Render("    "+line))
					}
				}
			}
			msgLines = append(msgLines, "")
		} else {
			verb := m.workingVerb
			if verb == "" {
				verb = pickSpyVerb()
				m.workingVerb = verb
			}
			msgLines = append(msgLines, assistantMsgStyle.Render("🕴️ "+verb))
			msgLines = append(msgLines, "")
		}
	}

	// Show pending schedules at the bottom.
	for _, sch := range m.schedules {
		if sch.Status != "pending" {
			continue
		}
		schedTime := sch.ScheduledAt
		// Try to format nicely.
		if t, err := time.Parse(time.RFC3339, sch.ScheduledAt); err == nil {
			schedTime = t.Local().Format("Jan 2, 3:04 PM")
		}
		schedWidth := m.width - 8
		if schedWidth < 20 {
			schedWidth = 60
		}
		schedStyle := lipgloss.NewStyle().Foreground(colorWarning)
		prefix := fmt.Sprintf("  ⏰ %s — ", schedTime)
		wrapped := wordWrap(sch.Prompt, schedWidth)
		for i, line := range strings.Split(wrapped, "\n") {
			if i == 0 {
				msgLines = append(msgLines, schedStyle.Render(prefix+line))
			} else {
				msgLines = append(msgLines, schedStyle.Render("    "+line))
			}
		}
	}

	// Show active subagents at the bottom (hide idle/completed; use esc-a to see all).
	if len(m.subagents) > 0 {
		subStyle := lipgloss.NewStyle().Foreground(colorPrimary)
		for i, sub := range m.subagents {
			if strings.Contains(sub.Status, "completed") {
				continue
			}
			name := sub.Name
			if name == "" {
				name = sub.SessionID[:12] + "..."
			}
			num := fmt.Sprintf("%d", i+1)
			if sub.Yolo {
				num = fmt.Sprintf("00%d", i+1)
			}
			line := subStyle.Render(fmt.Sprintf("  🕴️ %s %s [%s]", num, name, sub.Status))
			msgLines = append(msgLines, line)
		}
	}

	// Apply scroll and show only what fits.
	if msgHeight < 1 {
		msgHeight = 20
	}
	totalLines := len(msgLines)
	end := totalLines - m.scroll
	if end < 0 {
		end = 0
	}
	start := end - msgHeight
	if start < 0 {
		start = 0
	}
	if end > totalLines {
		end = totalLines
	}

	for i := start; i < end; i++ {
		b.WriteString(msgLines[i])
		b.WriteString("\n")
	}

	// Pad remaining space.
	rendered := end - start
	for i := rendered; i < msgHeight; i++ {
		b.WriteString("\n")
	}

	// Error display
	if m.err != nil {
		errLine := lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("  Error: %v", m.err))
		b.WriteString(errLine)
		b.WriteString("\n")
	}

	// Subagent picker overlay
	if m.pickingSubagent {
		pickerLabel := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" Subagents: ")
		b.WriteString(pickerLabel)
		b.WriteString("\n")
		for i, sub := range m.subagents {
			name := sub.Name
			if name == "" {
				name = sub.SessionID[:12] + "..."
			}
			num := fmt.Sprintf("%d", i+1)
			if sub.Yolo {
				num = fmt.Sprintf("00%d", i+1)
			}
			statusStyle := lipgloss.NewStyle().Foreground(colorMuted)
			if sub.Status == "working" {
				statusStyle = lipgloss.NewStyle().Foreground(colorWarning)
			}
			line := fmt.Sprintf("  %s %s %s", num, name, statusStyle.Render("["+sub.Status+"]"))
			if i == m.subagentCursor {
				b.WriteString(sessionSelectedStyle.Render(line))
			} else {
				b.WriteString(sessionNormalStyle.Render(line))
			}
			b.WriteString("\n")
		}
		// "New subagent..." entry
		newLine := "  + New subagent..."
		if m.subagentCursor == len(m.subagents) {
			b.WriteString(sessionSelectedStyle.Render(newLine))
		} else {
			b.WriteString(sessionNormalStyle.Render(newLine))
		}
		b.WriteString("\n")
		if m.confirmDeleteSubagent {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDanger).Bold(true).Render(
				"  Press d again to confirm delete, any other key to cancel"))
			b.WriteString("\n")
		}
		return b.String()
	}

	if m.pickingSchedule {
		pending := m.pendingSchedules()
		pickerLabel := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" ⏰ Schedules: ")
		b.WriteString(pickerLabel)
		b.WriteString("\n")
		for i, sch := range pending {
			schedTime := sch.ScheduledAt
			if t, err := time.Parse(time.RFC3339, sch.ScheduledAt); err == nil {
				schedTime = t.Local().Format("Jan 2, 3:04 PM")
			}
			prompt := sch.Prompt
			if len(prompt) > 60 {
				prompt = prompt[:57] + "..."
			}
			line := fmt.Sprintf("  %s — %s", schedTime, prompt)
			if i == m.scheduleCursor {
				b.WriteString(sessionSelectedStyle.Render(line))
			} else {
				b.WriteString(sessionNormalStyle.Render(line))
			}
			b.WriteString("\n")
		}
		// "New schedule..." entry
		newLine := "  + New schedule..."
		if m.scheduleCursor == len(pending) {
			b.WriteString(sessionSelectedStyle.Render(newLine))
		} else {
			b.WriteString(sessionNormalStyle.Render(newLine))
		}
		b.WriteString("\n")
		if m.confirmDeleteSchedule {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDanger).Bold(true).Render(
				"  Press d again to confirm cancel, any other key to abort"))
			b.WriteString("\n")
		}
		return b.String()
	}

	// In-TUI file viewer overlay
	if m.viewingFile {
		label := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" 📄 " + m.viewFileName)
		commentCount := len(m.viewFileComments)
		if commentCount > 0 {
			label += lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render(
				fmt.Sprintf(" (%d comment%s)", commentCount, pluralS(commentCount)))
		}
		b.WriteString(label)
		b.WriteString("\n")

		// Line input mode.
		if m.viewFileMode == fileViewModeLineInput {
			action := "Comment on"
			if m.viewFilePendingLine == -1 {
				action = "Delete comment on"
			}
			b.WriteString("  " + lipgloss.NewStyle().Foreground(colorPrimary).Render(
				action+" line: ") + m.viewFileLineInput.Render() + "█")
			b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("Enter=confirm  Esc=cancel"))
			b.WriteString("\n")
			return b.String()
		}

		// Comment input mode.
		if m.viewFileMode == fileViewModeComment {
			lineNum := m.viewFilePendingLine
			b.WriteString("  " + lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(
				fmt.Sprintf("Comment on line %d", lineNum)))
			// Show code excerpt.
			if lineNum > 0 && lineNum <= len(m.viewFileLines) {
				code := strings.TrimSpace(m.viewFileLines[lineNum-1])
				maxW := m.width - 30
				if maxW < 40 {
					maxW = 40
				}
				if len(code) > maxW {
					code = code[:maxW] + "..."
				}
				b.WriteString(" " + lipgloss.NewStyle().Foreground(colorMuted).Render(code))
			}
			inputWidth := m.width - 6
			if inputWidth < 20 {
				inputWidth = 20
			}
			wrappedLines := m.viewFileCommentInput.RenderWrapped(inputWidth, 2)
			for _, line := range wrappedLines {
				b.WriteString("\n  " + fieldActiveStyle.Render(line))
			}
			b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render(
				"Enter=save  Ctrl+J=newline  Esc=cancel"))
			b.WriteString("\n")
			return b.String()
		}

		// Submit review mode.
		if m.viewFileMode == fileViewModeSubmit {
			b.WriteString("  " + lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(
				"Submit review comments?"))
			b.WriteString("\n")
			for _, lineNum := range m.sortedFileCommentLines() {
				c := m.viewFileComments[lineNum]
				preview := c.comment
				if len(preview) > 50 {
					preview = preview[:50] + "..."
				}
				loc := fmt.Sprintf("line %d", lineNum)
				b.WriteString(fmt.Sprintf("  %s  %s\n",
					lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA")).Render(loc),
					lipgloss.NewStyle().Foreground(colorMuted).Render(preview)))
			}
			b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorPrimary).Render(
				"Overall comment (optional): ") + fieldActiveStyle.Render(m.viewFileReviewPrompt.Render()))
			b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render(
				"Enter=submit  Esc=cancel"))
			b.WriteString("\n")
			return b.String()
		}

		// Normal file view mode.
		viewHeight := m.height - 4
		if viewHeight < 1 {
			viewHeight = 20
		}
		contentWidth := m.width - 8 // 6 for line number gutter + comment marker + margin
		if contentWidth < 20 {
			contentWidth = 20
		}

		maxScroll := len(m.viewFileLines) - viewHeight
		if maxScroll < 0 {
			maxScroll = 0
		}

		end := m.viewFileScroll + viewHeight
		if end > len(m.viewFileLines) {
			end = len(m.viewFileLines)
		}

		gutterStyle := lipgloss.NewStyle().Foreground(colorMuted)
		lineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0"))
		commentMarkerStyle := lipgloss.NewStyle().Foreground(colorWarning).Render("*")

		for i := m.viewFileScroll; i < end; i++ {
			lineNum := gutterStyle.Render(fmt.Sprintf("%4d", i+1))
			marker := " "
			if _, hasComment := m.viewFileComments[i+1]; hasComment {
				marker = commentMarkerStyle
			}
			line := m.viewFileLines[i]
			if len(line) > contentWidth {
				line = line[:contentWidth]
			}
			b.WriteString(lineNum + marker + " " + lineStyle.Render(line))
			b.WriteString("\n")
		}

		status := lipgloss.NewStyle().Foreground(colorMuted)
		hints := "r=comment  "
		if commentCount > 0 {
			hints += "d=delete  s=submit  "
		}
		if len(m.viewFileLines) > viewHeight {
			b.WriteString(status.Render(fmt.Sprintf(
				"  line %d-%d of %d  %sEsc=back", m.viewFileScroll+1, end, len(m.viewFileLines), hints)))
		} else {
			b.WriteString(status.Render("  " + hints + "Esc=back"))
		}
		b.WriteString("\n")
		return b.String()
	}

	// Download folder picker overlay
	if m.downloadMode {
		label := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(
			" 📥 Download to: " + m.downloadPath)
		b.WriteString(label)
		b.WriteString("\n")
		if m.downloadLoading {
			b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  Loading..."))
			b.WriteString("\n")
		} else if m.downloadErr != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("  Error: %v", m.downloadErr)))
			b.WriteString("\n")
		} else {
			idx := 0
			hasParent := m.downloadPath != "/" && m.downloadPath != ""
			if hasParent {
				line := "  📁 .."
				if idx == m.downloadCursor {
					b.WriteString(sessionSelectedStyle.Render(line))
				} else {
					b.WriteString(sessionNormalStyle.Render(line))
				}
				b.WriteString("\n")
				idx++
			}
			dirs := m.downloadDirs()
			for _, entry := range dirs {
				line := fmt.Sprintf("  📁 %s", entry.Name)
				if idx == m.downloadCursor {
					b.WriteString(sessionSelectedStyle.Render(line))
				} else {
					b.WriteString(sessionNormalStyle.Render(line))
				}
				b.WriteString("\n")
				idx++
			}
			if len(dirs) == 0 && !hasParent {
				b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  (no subdirectories)"))
				b.WriteString("\n")
			}
		}
		hint := lipgloss.NewStyle().Foreground(colorMuted).Render("  Space=download here  Enter=open folder  Esc=cancel")
		b.WriteString(hint)
		b.WriteString("\n")
		return b.String()
	}

	// File browser overlay
	if m.browsingFiles {
		label := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" 📂 " + m.browserPath)
		b.WriteString(label)
		b.WriteString("\n")
		if m.browserLoading {
			b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  Loading..."))
			b.WriteString("\n")
		} else if m.browserErr != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("  Error: %v", m.browserErr)))
			b.WriteString("\n")
		} else {
			idx := 0
			hasParent := m.browserPath != "/" && m.browserPath != ""
			if hasParent {
				line := "  📁 .."
				if idx == m.browserCursor {
					b.WriteString(sessionSelectedStyle.Render(line))
				} else {
					b.WriteString(sessionNormalStyle.Render(line))
				}
				b.WriteString("\n")
				idx++
			}
			for _, entry := range m.browserEntries {
				icon := "📄"
				if entry.IsDir {
					icon = "📁"
				}
				line := fmt.Sprintf("  %s %s", icon, entry.Name)
				if idx == m.browserCursor {
					b.WriteString(sessionSelectedStyle.Render(line))
				} else {
					b.WriteString(sessionNormalStyle.Render(line))
				}
				b.WriteString("\n")
				idx++
			}
			if len(m.browserEntries) == 0 {
				b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  (empty directory)"))
				b.WriteString("\n")
			}
		}
		hint := lipgloss.NewStyle().Foreground(colorMuted).Render("  Enter=open app  h=view in Hem  d=download  c=copy path  Esc=back")
		b.WriteString(hint)
		b.WriteString("\n")
		return b.String()
	}

	// Create subagent prompt
	if m.creatingSubagent {
		label := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" New subagent prompt: ")
		cursor := "█"
		displayInput := m.subagentPrompt[:m.subagentPromptPos] + cursor + m.subagentPrompt[m.subagentPromptPos:]
		b.WriteString(label + displayInput)
		return b.String()
	}

	// Input line
	if m.scheduling {
		var label string
		if m.scheduleAt == "" {
			label = " ⏰ When? (e.g. +2h, 15:04, 2026-03-07T15:00:00Z): "
		} else {
			label = fmt.Sprintf(" ⏰ [%s] Prompt: ", m.scheduleAt)
		}
		schedLabel := lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render(label)
		b.WriteString(schedLabel + m.chatInput.Render())
	} else if m.commandMode {
		cmdBar := lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render(" COMMAND MODE ")
		if m.confirmDelete {
			cmdBar += lipgloss.NewStyle().Foreground(colorDanger).Bold(true).Render(
				"  Press d again to confirm delete, any other key to cancel")
		}
		b.WriteString(cmdBar)
	} else {
		prompt := inputPromptStyle.Render(" > ")
		if m.sending {
			b.WriteString(prompt)
		} else {
			lines := m.chatInput.RenderWrapped(inputWidth, 3)
			for i, line := range lines {
				if i == 0 {
					b.WriteString(prompt + line)
				} else {
					b.WriteString("\n   " + line)
				}
			}
		}
	}

	return b.String()
}

// recentTurns returns the most recently fetched page of turns (the tail of the conversation).
func (m chatModel) recentTurns() []conversationTurn {
	if m.recentCount <= 0 || m.recentCount >= len(m.conversation) {
		return m.conversation
	}
	return m.conversation[len(m.conversation)-m.recentCount:]
}

// renderMarkdown renders markdown content using glamour.
// cachedRenderMarkdown returns cached rendered markdown, or renders and caches it.
// Cache key includes width so width changes produce fresh renders.
func (m chatModel) cachedRenderMarkdown(content string, width int) string {
	if m.renderCache == nil {
		return renderMarkdown(content, width)
	}
	key := fmt.Sprintf("%d\x00%s", width, content)
	if cached, ok := m.renderCache[key]; ok {
		return cached
	}
	rendered := renderMarkdown(content, width)
	m.renderCache[key] = rendered
	return rendered
}

func renderMarkdown(content string, width int) string {
	if strings.TrimSpace(content) == "" {
		return content
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return wordWrap(content, width)
	}
	out, err := r.Render(content)
	if err != nil {
		return wordWrap(content, width)
	}
	// Trim trailing whitespace from glamour output.
	out = strings.TrimRight(out, "\n ")
	// If glamour produced empty output from non-empty content, fall back.
	if strings.TrimSpace(out) == "" {
		return wordWrap(content, width)
	}
	return out
}

// wordLeft returns the cursor position after moving one word to the left.
func wordLeft(s string, pos int) int {
	if pos == 0 {
		return 0
	}
	// Skip whitespace going left.
	i := pos
	for i > 0 {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		r, _ := utf8.DecodeLastRuneInString(s[:i])
		if r != ' ' && r != '\n' && r != '\t' {
			break
		}
		i -= size
	}
	// Skip word chars going left.
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if r == ' ' || r == '\n' || r == '\t' {
			break
		}
		i -= size
		_ = r
	}
	return i
}

// wordRight returns the cursor position after moving one word to the right.
func wordRight(s string, pos int) int {
	if pos >= len(s) {
		return len(s)
	}
	i := pos
	// Skip word chars going right.
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == ' ' || r == '\n' || r == '\t' {
			break
		}
		i += size
	}
	// Skip whitespace going right.
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != ' ' && r != '\n' && r != '\t' {
			break
		}
		i += size
	}
	return i
}

// wordWrap wraps text at the given width, breaking on spaces.
func wordWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	// Expand tabs to spaces (4-space tab stops) for consistent rendering.
	s = expandTabs(s, 4)

	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if lipgloss.Width(line) <= width {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			continue
		}
		// Preserve leading whitespace.
		trimmed := strings.TrimLeft(line, " ")
		indent := line[:len(line)-len(trimmed)]

		// Split the trimmed part into words, preserving inter-word spacing.
		words := strings.Fields(trimmed)
		currentLine := indent
		for _, word := range words {
			if currentLine == indent {
				// First word on this line (after indent).
				currentLine += word
			} else if lipgloss.Width(currentLine+" "+word) <= width {
				currentLine += " " + word
			} else {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString(currentLine)
				currentLine = indent + word
			}
		}
		if currentLine != "" {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(currentLine)
		}
	}
	return result.String()
}

// expandTabs replaces tab characters with spaces aligned to tabSize-column boundaries.
func expandTabs(s string, tabSize int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var result strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			spaces := tabSize - (col % tabSize)
			if spaces == 0 {
				spaces = tabSize
			}
			result.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else if r == '\n' {
			result.WriteRune(r)
			col = 0
		} else {
			result.WriteRune(r)
			col++
		}
	}
	return result.String()
}

var spyVerbs = []string{"Infiltrating...", "Surveilling...", "Decrypting...", "On a mission...", "Going undercover...", "Acquiring intel...", "Intercepting...", "Extracting..."}

func pickSpyVerb() string {
	return spyVerbs[rand.Intn(len(spyVerbs))]
}

// localTime parses a UTC timestamp string and returns it formatted in local time.
func localTime(s string) string {
	for _, layout := range []string{
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Local().Format("15:04:05")
		}
	}
	return s
}
