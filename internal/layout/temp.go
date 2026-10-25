package layout

// Temporary-file names. A writer that stages a file for an atomic rename, or a
// staging directory, builds the name with one of these helpers. Every helper
// puts TmpMarker in the name, and the table has one class-4 entry for the
// marker, so a name is class 4 whatever its base, directory or writer. The
// marker, not a list of base names, is what the table depends on: a helper
// given a base the table has never heard of still classifies. A writer that
// needs a new shape adds a helper that includes TmpMarker.

// TmpMarker is in the name of every staging file and directory ethos creates
// inside a working tree. It is not a suffix a real file would carry.
const TmpMarker = ".ethos-tmp"

// TmpPath is the staging name for an atomic write to dest: dest + TmpMarker.
func TmpPath(dest string) string { return dest + TmpMarker }

// TmpPattern is an os.CreateTemp pattern ending in TmpMarker, such as
// "record-*.yaml.ethos-tmp" for TmpPattern("record", ".yaml").
func TmpPattern(prefix, ext string) string { return prefix + "-*" + ext + TmpMarker }

// DotTmp is the hidden staging name for a file called name: ".name.ethos-tmp".
func DotTmp(name string) string { return "." + name + TmpMarker }

// SeedTmpPattern is the os.CreateTemp pattern the seeder uses beside base.
func SeedTmpPattern(base string) string { return base + ".seed.*" + TmpMarker }

// DotTempPattern is the os.CreateTemp pattern for a hidden sibling of the file
// called base: ".base.*.ethos-tmp".
func DotTempPattern(base string) string { return "." + base + ".*" + TmpMarker }

// SiblingTempPattern is the os.CreateTemp pattern for a sibling of the file
// called base: "base.*.ethos-tmp".
func SiblingTempPattern(base string) string { return base + ".*" + TmpMarker }

// MigrateStagingPrefix is the os.MkdirTemp prefix of the directory
// `ethos mission migrate` stages mission id in before renaming it into place.
func MigrateStagingPrefix(id string) string { return "." + id + ".migrate" + TmpMarker + "-" }
