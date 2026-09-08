package handler

import (
	"strings"
	"testing"
)

func TestNotifyUserTag(t *testing.T) {
	content := "Checking credentials. <NOTIFY_USER>Sign in to GitHub in the browser window.</NOTIFY_USER> Continuing."
	matches := notifyUserTagRe.FindAllStringSubmatch(content, -1)
	if len(matches) != 1 || matches[0][1] != "Sign in to GitHub in the browser window." {
		t.Fatalf("matches = %#v", matches)
	}
	if got := strings.TrimSpace(notifyUserTagRe.ReplaceAllString(content, "")); got != "Checking credentials.  Continuing." {
		t.Fatalf("cleaned content = %q", got)
	}
}

func TestNotifyUserTagRejectsOversizedMessage(t *testing.T) {
	content := "<NOTIFY_USER>" + strings.Repeat("x", 1001) + "</NOTIFY_USER>"
	if notifyUserTagRe.MatchString(content) {
		t.Fatal("oversized notification was accepted")
	}
}
