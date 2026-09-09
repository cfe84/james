package agent

import (
	"strings"
	"testing"
)

func TestGadgetCommandGrantsWithoutFilesystemAccess(t *testing.T) {
	for _, name := range []string{"claude", "copilot"} {
		params := RunParams{Agent: name, Prompt: "hello", Environment: map[string]string{"JAMES_GADGETS_TOKEN": "session-secret"}}
		for _, build := range []func(RunParams) agentInvocation{buildArgs, buildOneShotArgs} {
			args := strings.Join(build(params).args, " ")
			if !strings.Contains(args, "gadgets:*") || strings.Contains(args, "--add-dir") {
				t.Fatalf("%s gadget grant missing or grants memory files: %s", name, args)
			}
		}
		params.Environment = nil
		if args := gadgetAccessArgs(name, params); len(args) != 0 {
			t.Fatal("unauthenticated run received tool grant")
		}
	}
}

func TestGadgetEnvironmentDropsInheritedCredentialsAndRoutes(t *testing.T) {
	env := withEnvironment([]string{"JAMES_HEM_SOCKET=private", "JAMES_GADGETS_TOKEN=old", "KEEP=yes"}, map[string]string{
		"JAMES_GADGETS_TOKEN": "fresh", "JAMES_GADGETS_URL": "http://127.0.0.1:1/gadgets",
	})
	got := strings.Join(env, "\n")
	if strings.Contains(got, "private") || strings.Contains(got, "=old") || !strings.Contains(got, "=fresh") || !strings.Contains(got, "KEEP=yes") {
		t.Fatal(got)
	}
}
