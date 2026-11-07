//go:build windows

package provenance

// Windows cannot confirm directory-entry durability through os.File.Sync.
// File data is synced before rename; the receipt must not claim more.
const directoryDurability = "unconfirmed"

func syncDirectory(string) error { return nil }
