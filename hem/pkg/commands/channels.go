package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
)

// channelInfoWire mirrors moneypenny's envelope.ChannelInfo.
type channelInfoWire struct {
	ID           int64  `json:"id"`
	SessionID    string `json:"session_id"`
	Provider     string `json:"provider"`
	TargetID     string `json:"target_id"`
	TargetLabel  string `json:"target_label"`
	Enabled      bool   `json:"enabled"`
	Mention      string `json:"mention"`
	AllowAnyone  bool   `json:"allow_anyone"`
	LastError    string `json:"last_error,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type channelProviderWire struct {
	Name      string `json:"name"`
	Search    bool   `json:"search"`
	ProvideID bool   `json:"provide_id"`
}

type channelTargetWire struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// resolveMoneypennyByFlag resolves a moneypenny from an explicit name, falling
// back to the configured default.
func (e *Executor) resolveMoneypennyByFlag(mpName string) (*store.Moneypenny, error) {
	if mpName == "" {
		mpName, _ = e.store.GetDefault("moneypenny")
	}
	if mpName == "" {
		return nil, fmt.Errorf("moneypenny is required (use -m or set a default)")
	}
	mp, err := e.store.GetMoneypenny(mpName)
	if err != nil {
		return nil, err
	}
	if mp == nil {
		return nil, fmt.Errorf("moneypenny %q not found", mpName)
	}
	return mp, nil
}

// resolveChannelMoneypenny resolves a moneypenny for provider-scoped channel
// operations: from a session (preferred, so UI in a chat context needs no mp
// name), else from an explicit name/default.
func (e *Executor) resolveChannelMoneypenny(mpName, sessionID string) (*store.Moneypenny, error) {
	if sessionID != "" {
		return e.resolveSessionMoneypenny(sessionID)
	}
	return e.resolveMoneypennyByFlag(mpName)
}

// ListChannelProviders lists the channel providers available on a moneypenny.
func (e *Executor) ListChannelProviders(args []string) *protocol.Response {
	var mpName, sessionID string
	_, err := parseFlagsFromArgs("list-channel-providers", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
		fs.StringVar(&sessionID, "session-id", "", "resolve moneypenny from this session")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	mp, err := e.resolveChannelMoneypenny(mpName, sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "list_channel_providers", map[string]interface{}{})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	var result struct {
		Providers []channelProviderWire `json:"providers"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}
	if len(result.Providers) == 0 {
		return protocol.OKResponse(TextResult{Message: "No channel providers available."})
	}
	var rows [][]string
	for _, p := range result.Providers {
		rows = append(rows, []string{p.Name, boolYesNo(p.Search), boolYesNo(p.ProvideID)})
	}
	return protocol.OKResponse(TableResult{
		Headers: []string{"Provider", "Search", "By-ID"},
		Rows:    rows,
	})
}

// SearchChannelTargets searches a provider for candidate targets.
func (e *Executor) SearchChannelTargets(args []string) *protocol.Response {
	var mpName, sessionID, provider, query string
	remaining, err := parseFlagsFromArgs("search-channel", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
		fs.StringVar(&sessionID, "session-id", "", "resolve moneypenny from this session")
		fs.StringVar(&provider, "provider", "teams", "channel provider (e.g. teams)")
		fs.StringVar(&query, "query", "", "search query")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if query == "" && len(remaining) > 0 {
		query = strings.TrimSpace(strings.Join(remaining, " "))
	}
	if query == "" {
		return protocol.ErrResponse("--query is required")
	}
	mp, err := e.resolveChannelMoneypenny(mpName, sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "search_channel_targets", map[string]interface{}{
		"provider": provider,
		"query":    query,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	var result struct {
		Targets []channelTargetWire `json:"targets"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}
	if len(result.Targets) == 0 {
		return protocol.OKResponse(TextResult{Message: "No targets found."})
	}
	var rows [][]string
	for _, t := range result.Targets {
		rows = append(rows, []string{t.ID, t.Label, t.Detail})
	}
	return protocol.OKResponse(TableResult{
		Headers: []string{"Target ID", "Label", "Detail"},
		Rows:    rows,
	})
}

// CreateChannel binds a session to an external communication channel.
func (e *Executor) CreateChannel(args []string) *protocol.Response {
	var sessionID, provider, targetID, targetLabel, mention string
	var allowAnyone bool
	remaining, err := parseFlagsFromArgs("create-channel", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.StringVar(&provider, "provider", "teams", "channel provider (e.g. teams)")
		fs.StringVar(&targetID, "target-id", "", "target id (e.g. Teams chat id)")
		fs.StringVar(&targetLabel, "label", "", "optional display label")
		fs.StringVar(&mention, "mention", "", "only forward messages containing this @name (stripped before forwarding); empty = forward all")
		fs.BoolVar(&allowAnyone, "allow-anyone", false, "forward messages from any sender (default: only the signed-in owner)")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if sessionID == "" && len(remaining) > 0 {
		sessionID = remaining[0]
		remaining = remaining[1:]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required")
	}
	if targetID == "" && len(remaining) > 0 {
		targetID = remaining[0]
	}
	if targetID == "" {
		return protocol.ErrResponse("--target-id is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	cmdData := map[string]interface{}{
		"session_id": sessionID,
		"provider":   provider,
		"target_id":  targetID,
	}
	if targetLabel != "" {
		cmdData["target_label"] = targetLabel
	}
	if mention != "" {
		cmdData["mention"] = mention
	}
	if allowAnyone {
		cmdData["allow_anyone"] = true
	}
	resp, err := e.sendCommand(ctx, mp, "create_channel", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	var result struct {
		Channel channelInfoWire `json:"channel"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}
	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Bound session %s to %s channel %q (channel #%d)",
			sessionID, result.Channel.Provider, result.Channel.TargetLabel, result.Channel.ID),
	})
}

// ListChannels lists channel bindings for a session (or all when omitted).
func (e *Executor) ListChannels(args []string) *protocol.Response {
	var sessionID string
	remaining, err := parseFlagsFromArgs("list-channel", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if sessionID == "" && len(remaining) > 0 {
		sessionID = remaining[0]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "list_channels", map[string]interface{}{
		"session_id": sessionID,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	var result struct {
		Channels []channelInfoWire `json:"channels"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}
	if len(result.Channels) == 0 {
		return protocol.OKResponse(TextResult{Message: "No channels found."})
	}
	var rows [][]string
	for _, c := range result.Channels {
		mention := c.Mention
		if mention != "" {
			mention = "@" + mention
		}
		senders := "owner"
		if c.AllowAnyone {
			senders = "anyone"
		}
		rows = append(rows, []string{
			strconv.FormatInt(c.ID, 10),
			c.Provider,
			c.TargetLabel,
			c.TargetID,
			boolYesNo(c.Enabled),
			mention,
			senders,
			c.LastError,
		})
	}
	return protocol.OKResponse(TableResult{
		Headers: []string{"ID", "Provider", "Label", "Target ID", "Enabled", "Mention", "Senders", "Error"},
		Rows:    rows,
	})
}

// DeleteChannel removes a channel binding.
func (e *Executor) DeleteChannel(args []string) *protocol.Response {
	var sessionID string
	remaining, err := parseFlagsFromArgs("delete-channel", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if len(remaining) == 0 {
		return protocol.ErrResponse("channel_id is required")
	}
	channelID, err := strconv.ParseInt(remaining[0], 10, 64)
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("invalid channel_id: %s", remaining[0]))
	}
	if sessionID == "" && len(remaining) > 1 {
		sessionID = remaining[1]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required (use --session-id)")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	ctx := context.Background()
	if _, err := e.sendCommand(ctx, mp, "delete_channel", map[string]interface{}{
		"channel_id": channelID,
	}); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	return protocol.OKResponse(TextResult{Message: fmt.Sprintf("Deleted channel #%d", channelID)})
}

// SetChannelEnabled enables or disables a channel binding.
func (e *Executor) SetChannelEnabled(args []string, enabled bool) *protocol.Response {
	var sessionID string
	remaining, err := parseFlagsFromArgs("set-channel-enabled", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if len(remaining) == 0 {
		return protocol.ErrResponse("channel_id is required")
	}
	channelID, err := strconv.ParseInt(remaining[0], 10, 64)
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("invalid channel_id: %s", remaining[0]))
	}
	if sessionID == "" && len(remaining) > 1 {
		sessionID = remaining[1]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required (use --session-id)")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	ctx := context.Background()
	if _, err := e.sendCommand(ctx, mp, "set_channel_enabled", map[string]interface{}{
		"channel_id": channelID,
		"enabled":    enabled,
	}); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	return protocol.OKResponse(TextResult{Message: fmt.Sprintf("Channel #%d %s", channelID, state)})
}

// UpdateChannel updates a channel's @mention gate and/or sender policy. A flag
// is only applied when explicitly provided, so unspecified attributes are left
// unchanged. Use --mention "" to clear an existing mention.
func (e *Executor) UpdateChannel(args []string) *protocol.Response {
	const unset = "\x00"
	var sessionID string
	mention := unset
	var allowAnyone, ownerOnly bool
	remaining, err := parseFlagsFromArgs("set-channel", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.StringVar(&mention, "mention", unset, "set @name gate (empty string clears it)")
		fs.BoolVar(&allowAnyone, "allow-anyone", false, "forward messages from any sender")
		fs.BoolVar(&ownerOnly, "owner-only", false, "forward only the signed-in owner's messages")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if len(remaining) == 0 {
		return protocol.ErrResponse("channel_id is required")
	}
	channelID, err := strconv.ParseInt(remaining[0], 10, 64)
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("invalid channel_id: %s", remaining[0]))
	}
	if sessionID == "" && len(remaining) > 1 {
		sessionID = remaining[1]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required (use --session-id)")
	}
	if allowAnyone && ownerOnly {
		return protocol.ErrResponse("--allow-anyone and --owner-only are mutually exclusive")
	}

	cmdData := map[string]interface{}{"channel_id": channelID}
	if mention != unset {
		cmdData["mention"] = mention
	}
	if allowAnyone {
		cmdData["allow_anyone"] = true
	} else if ownerOnly {
		cmdData["allow_anyone"] = false
	}
	if len(cmdData) == 1 {
		return protocol.ErrResponse("nothing to update (use --mention, --allow-anyone, or --owner-only)")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	ctx := context.Background()
	if _, err := e.sendCommand(ctx, mp, "update_channel", cmdData); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	return protocol.OKResponse(TextResult{Message: fmt.Sprintf("Updated channel #%d", channelID)})
}

func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
