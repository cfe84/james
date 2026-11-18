package questionnaire

import (
	"fmt"
	"strings"
)

const (
	Role          = "questionnaire"
	SubmittedRole = "questionnaire_submitted"
	DiscardedRole = "questionnaire_discarded"
	MaxPending    = 4
	MaxDefinition = 8192
	MaxAnswer     = 2000
)

type Definition struct {
	Title     string     `json:"title"`
	Questions []Question `json:"questions"`
}

type Question struct {
	Prompt  string   `json:"prompt"`
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`
}

type Card struct {
	ID int64 `json:"id"`
	Definition
}

type Answer struct {
	Choice *int   `json:"choice,omitempty"`
	Text   string `json:"text,omitempty"`
}

type Resolution struct {
	SessionID string   `json:"session_id"`
	ID        int64    `json:"id"`
	Action    string   `json:"action"`
	Answers   []Answer `json:"answers,omitempty"`
}

func (d *Definition) Validate() error {
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" || len(d.Title) > 160 || len(d.Questions) < 1 || len(d.Questions) > 8 {
		return fmt.Errorf("provide a title (up to 160 bytes) and 1–8 questions")
	}
	for i := range d.Questions {
		q := &d.Questions[i]
		q.Prompt = strings.TrimSpace(q.Prompt)
		if q.Prompt == "" || len(q.Prompt) > 600 {
			return fmt.Errorf("question %d needs a prompt of up to 600 bytes", i+1)
		}
		switch q.Type {
		case "yes_no", "text":
			if len(q.Options) != 0 {
				return fmt.Errorf("question %d does not accept options", i+1)
			}
		case "choice":
			if len(q.Options) < 2 || len(q.Options) > 8 {
				return fmt.Errorf("question %d needs 2–8 options", i+1)
			}
			seen := map[string]bool{}
			for j, option := range q.Options {
				option = strings.TrimSpace(option)
				key := strings.ToLower(option)
				if option == "" || len(option) > 160 || seen[key] {
					return fmt.Errorf("question %d needs distinct, nonempty options of up to 160 bytes", i+1)
				}
				seen[key] = true
				q.Options[j] = option
			}
		default:
			return fmt.Errorf("question %d type must be yes_no, choice or text", i+1)
		}
	}
	return nil
}

func (d Definition) Reply(answers []Answer) (string, error) {
	if len(answers) != len(d.Questions) {
		return "", fmt.Errorf("answer every question")
	}
	var reply strings.Builder
	fmt.Fprintf(&reply, "Questionnaire answers: %s", d.Title)
	for i, q := range d.Questions {
		answer := answers[i]
		text := strings.TrimSpace(answer.Text)
		if answer.Choice == nil {
			if text == "" || len(text) > MaxAnswer {
				return "", fmt.Errorf("question %d needs an answer of up to %d bytes", i+1, MaxAnswer)
			}
		} else {
			options := q.Options
			if q.Type == "yes_no" {
				options = []string{"Yes", "No"}
			}
			if q.Type == "text" || text != "" || *answer.Choice < 0 || *answer.Choice >= len(options) {
				return "", fmt.Errorf("question %d has an invalid choice", i+1)
			}
			text = options[*answer.Choice]
		}
		fmt.Fprintf(&reply, "\n\n%d. %s\nAnswer: %s", i+1, q.Prompt, text)
	}
	return reply.String(), nil
}
