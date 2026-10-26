package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"james/hem/pkg/store"
	"james/moneypenny/pkg/envelope"
)

type ponsonbiesModel struct {
	client                 *client
	stores                 []store.Ponsonby
	cursor, width, height  int
	loading, confirmDelete bool
	err                    error
	form                   *createModel
	detail                 *store.Ponsonby
}

type ponsonbiesLoadedMsg struct {
	stores []store.Ponsonby
	err    error
}
type ponsonbyActionMsg struct{ err error }
type ponsonbyDetailMsg struct {
	detail *store.Ponsonby
	err    error
}

func (c *client) ponsonby(verb string, args ...string) (json.RawMessage, error) {
	resp, err := c.send(verb, "ponsonby", args...)
	if err != nil {
		return nil, err
	}
	if resp.Status == "error" {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Data, nil
}

func (m ponsonbiesModel) load() tea.Cmd {
	return func() tea.Msg {
		raw, err := m.client.ponsonby("list")
		if err != nil {
			return ponsonbiesLoadedMsg{err: err}
		}
		var table struct {
			Rows [][]string `json:"rows"`
		}
		if err := json.Unmarshal(raw, &table); err != nil {
			return ponsonbiesLoadedMsg{err: err}
		}
		stores := []store.Ponsonby{}
		for _, row := range table.Rows {
			if len(row) != 7 {
				return ponsonbiesLoadedMsg{err: fmt.Errorf("invalid Ponsonby list row")}
			}
			stores = append(stores, store.Ponsonby{PonsonbyStore: envelope.PonsonbyStore{ID: row[0], Name: row[1], Purpose: row[2], Agent: row[5], Model: row[6]}, Moneypenny: row[3], Default: row[4] == "yes"})
		}
		return ponsonbiesLoadedMsg{stores: stores}
	}
}

func (m ponsonbiesModel) action(verb string, args ...string) tea.Cmd {
	return func() tea.Msg { _, err := m.client.ponsonby(verb, args...); return ponsonbyActionMsg{err: err} }
}

func (m ponsonbiesModel) edit(p *store.Ponsonby) *createModel {
	fields := []formField{
		{label: "Name", flag: "--name"},
		{label: "Purpose", flag: "--purpose"},
		{label: "Moneypenny", flag: "--moneypenny"},
		{label: "Agent (copilot, claude, opencode)", flag: "--agent", value: "copilot"},
		{label: "Model (required)", flag: "--model"},
		{label: "Default for new agents (shares transcripts)", flag: "--default", value: "false", isBool: true, explicitBool: true},
	}
	title := " New Ponsonby "
	id := ""
	if p != nil {
		title = " Edit Ponsonby: " + p.Name + " "
		id = p.ID
		fields = []formField{
			{label: "Purpose", flag: "--purpose", value: p.Purpose},
			{label: "Agent (copilot, claude, opencode)", flag: "--agent", value: p.Agent},
			{label: "Model (required)", flag: "--model", value: p.Model},
			{label: "Default for new agents (shares transcripts)", flag: "--default", value: fmt.Sprint(p.Default), isBool: true, explicitBool: true},
		}
	}
	for i := range fields {
		fields[i].cursorPos = len(fields[i].value)
	}
	c := m.client
	return &createModel{client: c, fields: fields, width: m.width, height: m.height, title: title, submit: func(fields []formField) tea.Msg {
		args := []string{}
		verb := "create"
		if id != "" {
			verb = "update"
			args = append(args, id)
		}
		for _, f := range fields {
			if strings.TrimSpace(f.value) == "" {
				return ponsonbyActionMsg{err: fmt.Errorf("%s is required", f.label)}
			}
			if f.isBool {
				args = append(args, f.flag+"="+f.value)
			} else {
				args = append(args, f.flag, f.value)
			}
		}
		_, err := c.ponsonby(verb, args...)
		return ponsonbyActionMsg{err: err}
	}}
}

func (m ponsonbiesModel) Update(msg tea.Msg) (ponsonbiesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case ponsonbiesLoadedMsg:
		m.loading = false
		m.stores = msg.stores
		m.err = msg.err
		m.cursor = min(m.cursor, max(0, len(m.stores)-1))
	case ponsonbyActionMsg:
		if msg.err != nil {
			m.err = msg.err
			if m.form != nil {
				m.form.err = msg.err
				m.form.creating = false
			}
			return m, nil
		}
		m.form = nil
		m.detail = nil
		m.err = nil
		return m, m.load()
	case ponsonbyDetailMsg:
		m.detail = msg.detail
		m.err = msg.err
	case tea.KeyMsg:
		if m.form != nil {
			if msg.String() == "esc" {
				m.form = nil
				return m, nil
			}
			form, cmd := m.form.Update(msg)
			m.form = &form
			return m, cmd
		}
		confirm := m.confirmDelete
		m.confirmDelete = false
		if msg.String() == "n" {
			m.form = m.edit(nil)
			m.err = nil
			return m, nil
		}
		if msg.String() == "r" {
			m.detail = nil
			return m, m.load()
		}
		if msg.String() == "up" || msg.String() == "k" {
			m.cursor = max(0, m.cursor-1)
			m.detail = nil
		}
		if msg.String() == "down" || msg.String() == "j" {
			m.cursor = min(max(0, len(m.stores)-1), m.cursor+1)
			m.detail = nil
		}
		if len(m.stores) == 0 {
			return m, nil
		}
		p := m.stores[m.cursor]
		switch msg.String() {
		case "e":
			m.form = m.edit(&p)
			m.err = nil
		case "s":
			return m, m.action("update", p.ID, fmt.Sprintf("--default=%t", !p.Default))
		case "d":
			if !confirm {
				m.confirmDelete = true
				return m, nil
			}
			return m, m.action("delete", p.ID)
		case "enter":
			return m, func() tea.Msg {
				raw, err := m.client.ponsonby("show", p.ID)
				if err != nil {
					return ponsonbyDetailMsg{err: err}
				}
				var detail store.Ponsonby
				if err := json.Unmarshal(raw, &detail); err != nil {
					return ponsonbyDetailMsg{err: err}
				}
				return ponsonbyDetailMsg{detail: &detail}
			}
		case "t":
			if m.detail != nil && m.detail.ID == p.ID {
				for _, j := range m.detail.Jobs {
					if j.Status == "failed" {
						return m, m.action("retry", m.detail.ID, "--request", j.ID)
					}
				}
			}
		}
	}
	return m, nil
}

func (m ponsonbiesModel) View() string {
	if m.form != nil {
		return m.form.View()
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Ponsonbies ") + "\n\n")
	b.WriteString("  Defaults share new agents' transcripts with the store host/provider.\n")
	if m.loading {
		b.WriteString("  Loading...\n")
	}
	if len(m.stores) == 0 && !m.loading {
		b.WriteString("  No shared stores. Press n to create one.\n")
	}
	start := max(0, m.cursor-max(1, m.height-12)+1)
	for i := start; i < len(m.stores) && i < start+max(1, m.height-12); i++ {
		p := m.stores[i]
		line := fmt.Sprintf("  %-20s %-20s %-24s default=%t", truncate(p.Name, 18), truncate(p.Moneypenny, 18), truncate(p.Model, 22), p.Default)
		if i == m.cursor {
			b.WriteString(sessionSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}
	if len(m.stores) > 0 {
		p := m.stores[m.cursor]
		b.WriteString("\n  " + p.Agent + ": " + truncate(p.Purpose, max(20, m.width-16)) + "\n")
	}
	if m.detail != nil {
		b.WriteString("\n  " + truncate(m.detail.Introduction, max(20, m.width-4)) + "\n")
		counts := map[string]int{}
		for _, j := range m.detail.Jobs {
			counts[j.Status]++
		}
		b.WriteString(fmt.Sprintf("  Requests: %d pending, %d running, %d done, %d failed\n", counts["pending"], counts["running"], counts["done"], counts["failed"]))
		for _, j := range m.detail.Jobs {
			if j.Status == "failed" {
				b.WriteString("  Failed: " + truncate(j.Error, max(20, m.width-12)) + " (t: retry)\n")
				break
			}
		}
	}
	if m.confirmDelete {
		b.WriteString("\n  Permanently delete this store? Press d again; any other key cancels.\n")
	}
	if m.err != nil {
		b.WriteString("\n  Error: " + m.err.Error() + "\n")
	}
	return b.String()
}
