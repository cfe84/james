package handler

import (
	"context"
	"syscall"
	"unsafe"

	"james/moneypenny/pkg/envelope"
)

var getProcessMemoryInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// PROCESS_MEMORY_COUNTERS_EX uses SIZE_T fields after its two DWORDs.
type processMemoryCounters struct {
	Size, PageFaultCount                               uint32
	PeakWorkingSetSize, WorkingSetSize                 uintptr
	QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage uintptr
	PagefileUsage, PeakPagefileUsage, PrivateUsage     uintptr
}

func processDiagnostics(_ context.Context) envelope.ProcessDiagnostics {
	if err := getProcessMemoryInfo.Find(); err != nil {
		return envelope.ProcessDiagnostics{Error: "process memory unsupported: K32GetProcessMemoryInfo unavailable"}
	}
	var counters processMemoryCounters
	counters.Size = uint32(unsafe.Sizeof(counters))
	ok, _, _ := getProcessMemoryInfo.Call(^uintptr(0), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if ok == 0 {
		return envelope.ProcessDiagnostics{Error: "process memory unavailable: K32GetProcessMemoryInfo failed"}
	}
	rss, private := uint64(counters.WorkingSetSize), uint64(counters.PrivateUsage)
	return envelope.ProcessDiagnostics{RSSBytes: &rss, PrivateBytes: &private}
}
