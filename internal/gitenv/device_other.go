//go:build !unix

package gitenv

// statDevice reports no device where there is no st_dev: the hand rules then
// never see a filesystem boundary.
func statDevice(string) (uint64, bool) { return 0, false }
