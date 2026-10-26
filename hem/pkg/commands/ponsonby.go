package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/moneypenny/pkg/envelope"
)

func (e *Executor) resolvePonsonbies(spec string, defaults bool) ([]envelope.Ponsonby, error) {
	stores, err := e.store.ListPonsonbies()
	if err != nil {
		return nil, err
	}
	selection := strings.Split(spec, ",")
	if spec == "none" {
		selection = nil
	}
	if defaults {
		selection = nil
		for _, p := range stores {
			if p.Default {
				selection = append(selection, p.ID)
			}
		}
	}
	out := []envelope.Ponsonby{}
	seen := map[string]bool{}
	for _, name := range selection {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		publish := true
		if strings.HasSuffix(name, ":read") {
			publish = false
			name = strings.TrimSuffix(name, ":read")
		}
		found := false
		for _, p := range stores {
			if name != p.ID && name != p.Name {
				continue
			}
			found = true
			if seen[p.ID] {
				return nil, fmt.Errorf("duplicate Ponsonby assignment %q", name)
			}
			seen[p.ID] = true
			out = append(out, envelope.Ponsonby{ID: p.ID, Name: p.Name, Purpose: p.Purpose, Introduction: p.Introduction, Publish: publish})
		}
		if !found {
			return nil, fmt.Errorf("Ponsonby store %q not found", name)
		}
	}
	if len(out) > 8 {
		return nil, fmt.Errorf("at most 8 Ponsonby stores may be attached")
	}
	return out, nil
}

func ponsonbySpec(attachments []envelope.Ponsonby) string {
	out := []string{}
	for _, a := range attachments {
		id := a.ID
		if !a.Publish {
			id += ":read"
		}
		out = append(out, id)
	}
	return strings.Join(out, ",")
}

func (e *Executor) PonsonbyCommand(verb string, args []string) *protocol.Response {
	var name, host, purpose, introduction, agent, model, requestID string
	var defaults bool
	remaining, err := parseFlagsFromArgs(verb+"-ponsonby", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "", "store name or ID")
		if verb == "create" || verb == "update" {
			fs.StringVar(&host, "moneypenny", "", "host Moneypenny")
			fs.StringVar(&purpose, "purpose", "", "what this store should remember")
			fs.StringVar(&agent, "agent", "copilot", "curator agent")
			fs.StringVar(&model, "model", "", "explicit inexpensive curator model")
			fs.BoolVar(&defaults, "default", false, "attach to new agents by default")
		}
		if verb == "create" {
			fs.StringVar(&introduction, "introduction", "", "initial knowledge introduction")
		}
		if verb == "retry" {
			fs.StringVar(&requestID, "request", "", "failed request ID to retry")
		}
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if name == "" && len(remaining) == 1 {
		name = remaining[0]
	}
	stores, err := e.store.ListPonsonbies()
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if verb == "list" {
		rows := [][]string{}
		for _, p := range stores {
			d := "no"
			if p.Default {
				d = "yes"
			}
			rows = append(rows, []string{p.ID, p.Name, p.Purpose, p.Moneypenny, d, p.Agent, p.Model})
		}
		return protocol.OKResponse(TableResult{Headers: []string{"ID", "Name", "Purpose", "Moneypenny", "Default", "Agent", "Model"}, Rows: rows})
	}
	var p store.Ponsonby
	found := false
	for _, s := range stores {
		if s.ID == name || s.Name == name {
			p = s
			found = true
			break
		}
	}
	if verb == "create" {
		if found {
			return protocol.ErrResponse("Ponsonby name already exists")
		}
		if strings.ContainsAny(name, ",:") || strings.TrimSpace(name) == "" || name == "none" {
			return protocol.ErrResponse("store name required, without comma or colon")
		}
		if purpose == "" || model == "" {
			return protocol.ErrResponse("create ponsonby requires --purpose and explicit --model")
		}
		mp, err := e.resolveMoneypennyForSession(host)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		p = store.Ponsonby{PonsonbyStore: envelope.PonsonbyStore{ID: generateSessionID(), Name: name, Purpose: purpose, Introduction: introduction, Agent: agent, Model: model},
			Moneypenny: mp.Name, Default: defaults}
	} else if !found {
		return protocol.ErrResponse("Ponsonby store not found")
	}
	if verb == "update" {
		if flagSeenIn(args, "purpose") {
			p.Purpose = purpose
		}
		if flagSeenIn(args, "model") {
			p.Model = model
		}
		if flagSeenIn(args, "agent") {
			p.Agent = agent
		}
		if flagSeenIn(args, "default") {
			p.Default = defaults
		}
		if flagSeenIn(args, "moneypenny") {
			return protocol.ErrResponse("moving stores between hosts is not supported")
		}
	}
	mp, err := e.store.GetMoneypenny(p.Moneypenny)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse("store host is not registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	method := "ponsonby_get"
	if verb == "create" || verb == "update" {
		method = "ponsonby_save"
	}
	if verb == "retry" {
		method = "ponsonby_retry"
		if requestID == "" {
			return protocol.ErrResponse("--request required")
		}
	}
	if verb == "delete" {
		attached, err := e.store.PonsonbyAssigned(p.ID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if attached {
			return protocol.ErrResponse("remove store assignments before deletion")
		}
		method = "ponsonby_delete"
	}
	request := envelope.PonsonbyRequest{Store: p.PonsonbyStore, Job: envelope.PonsonbyJob{ID: requestID, StoreID: p.ID}}
	resp, err := e.sendCommand(ctx, mp, method, request)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if verb == "retry" {
		return protocol.OKResponse(TextResult{Message: "Ponsonby request queued for retry."})
	}
	if verb == "delete" {
		if err := e.store.DeletePonsonby(p.ID); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return protocol.OKResponse(TextResult{Message: "Ponsonby store and memory deleted."})
	}
	if verb == "create" || verb == "update" {
		if err := e.store.SavePonsonby(p); err != nil {
			return protocol.ErrResponse(err.Error())
		}
	}
	var live envelope.PonsonbyStore
	if err := json.Unmarshal(resp.Data, &live); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	p.Introduction = live.Introduction
	p.Jobs = live.Jobs
	return protocol.OKResponse(p)
}

func (e *Executor) ponsonbyGadget(source *store.Session, method string, raw json.RawMessage) *protocol.Response {
	var data struct {
		Store string `json:"store"`
		ID    string `json:"id"`
		Body  string `json:"body"`
	}
	if err := decodeGadgetData(raw, &data); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	attachments, err := e.store.SessionPonsonbies(source.SessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	stores, err := e.store.ListPonsonbies()
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	results := []any{}
	for _, a := range attachments {
		if method != "ponsonby.list" && method != "ponsonby.ingest" && data.Store != a.ID && data.Store != a.Name {
			continue
		}
		if method == "ponsonby.ingest" && !a.Publish {
			continue
		}
		var p *store.Ponsonby
		for i := range stores {
			if stores[i].ID == a.ID {
				p = &stores[i]
				break
			}
		}
		if p == nil {
			return protocol.ErrResponse("assigned Ponsonby store no longer exists")
		}
		mp, err := e.store.GetMoneypenny(p.Moneypenny)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse("store host not registered")
		}
		request := envelope.PonsonbyRequest{Store: p.PonsonbyStore}
		target := "ponsonby_get"
		switch method {
		case "ponsonby.ask":
			request.Job = envelope.PonsonbyJob{ID: generateSessionID(), StoreID: p.ID, SourceID: source.SessionID, Kind: "ask", Body: data.Body}
			target = "ponsonby_submit"
		case "ponsonby.ingest":
			request.Job = envelope.PonsonbyJob{StoreID: p.ID, SourceID: source.SessionID, Kind: "ingest", Body: data.Body}
			// Include store ID in delivery identity so multiple attached stores
			// on one host never collide.
			request.Job.ID = p.ID + "-" + source.SessionID + "-" + data.ID
			target = "ponsonby_submit"
		case "ponsonby.result":
			request.Job = envelope.PonsonbyJob{ID: data.ID, StoreID: p.ID, SourceID: source.SessionID}
			target = "ponsonby_result"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := e.sendCommand(ctx, mp, target, request)
		cancel()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		var result any
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if method == "ponsonby.list" {
			var live envelope.PonsonbyStore
			if err := json.Unmarshal(resp.Data, &live); err != nil {
				return protocol.ErrResponse(err.Error())
			}
			p.Introduction = live.Introduction
			if err := e.store.SavePonsonby(*p); err != nil {
				return protocol.ErrResponse(err.Error())
			}
			results = append(results, envelope.Ponsonby{ID: p.ID, Name: p.Name, Purpose: boundedPonsonbyText(p.Purpose, 256), Introduction: boundedPonsonbyText(live.Introduction, 1000), Publish: a.Publish})
			continue
		}

		if method == "ponsonby.ingest" {
			continue
		}
		return protocol.OKResponse(result)
	}
	if method == "ponsonby.list" {
		return protocol.OKResponse(map[string]any{"stores": results})
	}
	if method == "ponsonby.ingest" {
		return protocol.OKResponse(map[string]bool{"accepted": true})
	}
	return protocol.ErrResponse("store is not assigned to this session")
}

func boundedPonsonbyText(text string, maxEncoded int) string {
	runes := []rune(text)
	for len(runes) > 0 {
		raw, _ := json.Marshal(string(runes))
		if len(raw) <= maxEncoded {
			return string(runes)
		}
		runes = runes[:len(runes)/2]
	}
	return ""
}
