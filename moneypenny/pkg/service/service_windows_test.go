//go:build windows

package service

import (
	"os"
	"strings"
	"testing"
)

func TestWindowsTaskWrapperSavesCrashLogAndExitCode(t *testing.T) {
	dir := t.TempDir()
	logPath := `C:\Users\Jane Doe\.config\james\moneypenny\moneypenny.log`
	wrapperPath, err := writeTaskWrapper(&Config{
		BinaryPath: `C:\Program Files\James\moneypenny.exe`,
		DataDir:    dir,
		LogFile:    logPath,
		Local:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := string(data)
	for _, want := range []string{
		`set "exitCode=%ERRORLEVEL%"`,
		`if "%exitCode%"=="0" exit /b 0`,
		`crash-logs`,
		`Copy-Item -LiteralPath $logPath`,
		`moneypenny-crash-$timestamp-exit-$exitCode.log`,
		`Select-Object -Skip 10`,
		`exit /b %exitCode%`,
	} {
		if !strings.Contains(wrapper, want) {
			t.Fatalf("task wrapper missing %q:\n%s", want, wrapper)
		}
	}
}
