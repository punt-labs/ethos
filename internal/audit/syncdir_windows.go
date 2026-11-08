//go:build windows

package audit

// Windows does not support syncing a directory through os.File. Payload files
// are still synced before atomic installation; directory durability is limited
// to the guarantees of the filesystem's rename/link implementation.
func syncAuditDir(string) error { return nil }
