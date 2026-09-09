package agent

import (
	"testing"
)

func argsContainPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func argsContain(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

func TestBuildArgsDoNotGrantNativeMemoryAccess(t *testing.T) {
	for _, yolo := range []bool{false, true} {
		params := RunParams{Prompt: "hi", SessionDir: "/data/sessions/s1", Yolo: yolo}
		for _, inv := range []agentInvocation{buildClaudeArgs(params), buildCopilotArgs(params), buildOpenCodeArgs(params)} {
			for _, flag := range []string{"--add-dir", "--allowedTools", "--allow-tool=write"} {
				if argsContain(inv.args, flag) {
					t.Errorf("unexpected native memory permission %s: %v", flag, inv.args)
				}
			}
		}
	}
}
