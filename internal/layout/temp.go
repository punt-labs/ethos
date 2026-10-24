package layout

// Temporary-file names. A writer that stages a file for an atomic rename, or a
// staging directory, builds the name with one of these helpers, and the
// class-4 entries of Table are built from the same helpers, so the name a
// writer creates and the pattern that classifies it cannot drift apart. A
// writer that needs a new shape adds a helper and a Temp entry here.

// TmpSuffix ends every staged file whose name is a pattern or a destination
// plus a suffix.
const TmpSuffix = ".tmp"

// TmpPath is the staging name for an atomic write to dest: dest + ".tmp".
func TmpPath(dest string) string { return dest + TmpSuffix }

// TmpPattern is an os.CreateTemp pattern ending in TmpSuffix, such as
// "record-*.yaml.tmp" for TmpPattern("record", ".yaml").
func TmpPattern(prefix, ext string) string { return prefix + "-*" + ext + TmpSuffix }

// DotTmp is the hidden staging name for a file called name: ".name.tmp".
func DotTmp(name string) string { return "." + name + TmpSuffix }

// SeedTmpPattern is the os.CreateTemp pattern the seeder uses beside base.
func SeedTmpPattern(base string) string { return base + ".seed.*" + TmpSuffix }

// DotTempPattern is the os.CreateTemp pattern for a hidden sibling of the file
// called base: ".base.*".
func DotTempPattern(base string) string { return "." + base + ".*" }

// SiblingTempPattern is the os.CreateTemp pattern for a sibling of the file
// called base: "base.*".
func SiblingTempPattern(base string) string { return base + ".*" }

// MigrateStagingPrefix is the os.MkdirTemp prefix of the directory
// `ethos mission migrate` stages mission id in before renaming it into place.
func MigrateStagingPrefix(id string) string { return "." + id + ".migrate-" }
