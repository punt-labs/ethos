//go:build windows

package process

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readProc returns the parent PID and command name (the executable's base
// file name, e.g. "claude.exe") for the given PID. Windows has no /proc to
// read a single process's stat from directly; CreateToolhelp32Snapshot
// takes a snapshot of every running process and both the ppid and the exe
// name come from the same entry in one walk.
func readProc(pid int) (ppid int, comm string, err error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, "", fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for walkErr := windows.Process32First(snapshot, &entry); walkErr == nil; walkErr = windows.Process32Next(snapshot, &entry) {
		if int(entry.ProcessID) != pid {
			continue
		}
		return int(entry.ParentProcessID), windows.UTF16ToString(entry.ExeFile[:]), nil
	}
	return 0, "", fmt.Errorf("pid %d not found in process snapshot", pid)
}
