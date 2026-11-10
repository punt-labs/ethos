package mission

// FailDirSyncForTest makes every directory sync report err until the returned
// function is called. It exists so a caller outside this package can drive the
// durability-warning path, which a real filesystem does not produce on demand.
func FailDirSyncForTest(err error) (restore func()) {
	real := syncDir
	syncDir = func(string) error { return err }
	return func() { syncDir = real }
}
