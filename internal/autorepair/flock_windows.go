//go:build windows

package autorepair

import "os"

// supported is false: Windows is not a shipped target (DESIGN.md DES-084), so
// Ensure compiles there and does nothing.
const supported = false

func tryLock(*os.File) (bool, error) { return false, nil }

func unlock(*os.File) error { return nil }
