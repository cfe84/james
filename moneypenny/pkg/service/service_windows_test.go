//go:build windows

package service

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestTaskDefinitionUsesUTF16LEWithBOM(t *testing.T) {
	data := utf16LEWithBOM(`<?xml version="1.0" encoding="UTF-16"?><Task/>`)
	if len(data) < 2 || binary.LittleEndian.Uint16(data[:2]) != 0xFEFF {
		t.Fatalf("task XML missing UTF-16LE BOM")
	}
	if got := string(utf16.Decode(bytesToUTF16(data[2:]))); got != `<?xml version="1.0" encoding="UTF-16"?><Task/>` {
		t.Fatalf("task XML encoding round trip = %q", got)
	}
}

func bytesToUTF16(data []byte) []uint16 {
	out := make([]uint16, len(data)/2)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return out
}

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
