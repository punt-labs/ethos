package testhelpers

import "os"

const ProcessCoverDir = "ETHOS_TEST_COVERDIR"

func CoverBuild(args ...string) []string {
	build := []string{"build"}
	if os.Getenv(ProcessCoverDir) != "" {
		build = append(build, "-cover", "-covermode=atomic")
	}
	return append(build, args...)
}

func CoverEnv(env []string) []string {
	if dir := os.Getenv(ProcessCoverDir); dir != "" {
		if env == nil {
			env = os.Environ()
		}
		return append(env, "GOCOVERDIR="+dir)
	}
	return env
}

func EnableInheritedCoverage() {
	if dir := os.Getenv(ProcessCoverDir); dir != "" {
		if err := os.Setenv("GOCOVERDIR", dir); err != nil {
			panic(err)
		}
	}
}
