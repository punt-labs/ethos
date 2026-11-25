package audit

import "os"

func appendFileIdentity(os.FileInfo) (dev, ino uint64, ok bool) {
	return 0, 0, false
}
