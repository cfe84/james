package service

import (
	"strings"
	"testing"
)

func TestWindowsTaskDefinitionRestartsUnexpectedFailures(t *testing.T) {
	definition, err := windowsTaskDefinition(&Config{UserLevel: true}, `C:\Users\Jane & John\moneypenny-service.vbs`, `CONTOSO\Jane & John`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<UserId>CONTOSO\Jane &amp; John</UserId>`,
		`<LogonTrigger><Enabled>true</Enabled><UserId>CONTOSO\Jane &amp; John</UserId></LogonTrigger>`,
		`<StartWhenAvailable>true</StartWhenAvailable>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<Interval>PT1M</Interval>`,
		`<Count>999</Count>`,
		`CONTOSO\Jane &amp; John`,
		`C:\Users\Jane &amp; John\moneypenny-service.vbs`,
	} {
		if !strings.Contains(definition, want) {
			t.Fatalf("task definition missing %q:\n%s", want, definition)
		}
	}
}

func TestWindowsSystemTaskDefinitionUsesSystemAccount(t *testing.T) {
	definition, err := windowsTaskDefinition(&Config{}, `C:\ProgramData\James\moneypenny-service.vbs`, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<UserId>S-1-5-18</UserId>`,
		`<BootTrigger><Enabled>true</Enabled></BootTrigger>`,
		`<LogonType>ServiceAccount</LogonType>`,
		`<RunLevel>HighestAvailable</RunLevel>`,
	} {
		if !strings.Contains(definition, want) {
			t.Fatalf("system task definition missing %q:\n%s", want, definition)
		}
	}
}
