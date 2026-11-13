package agent

import (
	"path/filepath"
	"reflect"
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

func TestCopilotAttachmentRouting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	otherDir := filepath.Join(t.TempDir(), "attachments")
	for _, tc := range []struct {
		name        string
		files       []string
		nativeFiles []string
		dirs        []string
	}{
		{
			name:        "native images and documents",
			files:       []string{filepath.Join(dir, "photo.PNG"), filepath.Join(dir, "report.pdf"), filepath.Join(dir, "notes.docx")},
			nativeFiles: []string{filepath.Join(dir, "photo.PNG"), filepath.Join(dir, "report.pdf"), filepath.Join(dir, "notes.docx")},
		},
		{
			name: "mixed uploads",
			files: []string{
				filepath.Join(dir, "logs.ZIP"), filepath.Join(dir, "photo.png"),
				filepath.Join(dir, "notes.txt"), filepath.Join(otherDir, "data.csv"),
				filepath.Join(otherDir, "unknown"), filepath.Join(otherDir, "diagram.svg"),
			},
			nativeFiles: []string{filepath.Join(dir, "photo.png")},
			dirs:        []string{dir, otherDir},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := buildCopilotArgs(RunParams{Prompt: "Inspect the attached files.", Attachments: tc.files})
			var nativeFiles, dirs []string
			for i := 0; i+1 < len(inv.args); i++ {
				switch inv.args[i] {
				case "--attachment":
					nativeFiles = append(nativeFiles, inv.args[i+1])
				case "--add-dir":
					dirs = append(dirs, inv.args[i+1])
				}
			}
			if !reflect.DeepEqual(nativeFiles, tc.nativeFiles) || !reflect.DeepEqual(dirs, tc.dirs) {
				t.Fatalf("attachment routing: native=%v dirs=%v, want native=%v dirs=%v",
					nativeFiles, dirs, tc.nativeFiles, tc.dirs)
			}
		})
	}
}
