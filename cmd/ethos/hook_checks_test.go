//go:build linux || darwin

package main

import (
 "os"
 "path/filepath"
 "testing"

 "github.com/stretchr/testify/require"
)

func TestCommitChecks(t *testing.T) {
 for _, tc := range []struct{name, mode string; code int}{
  {"default blocks", "", 2}, {"advisory continues", "    on_fail: advisory\n", 0},
 } {
  t.Run(tc.name, func(t *testing.T) {
   se := enabledHookRepo(t)
   require.NoError(t, os.WriteFile(filepath.Join(se.repo,".punt-labs/ethos.yaml"), []byte("checks: project\n"),0600))
   dir := filepath.Join(se.repo,".punt-labs/ethos/checks")
   require.NoError(t,os.MkdirAll(dir,0700))
   require.NoError(t,os.WriteFile(filepath.Join(dir,"project.yaml"),[]byte("name: project\nchecks:\n  - name: commit-probe\n    stage: commit\n    files: all\n    run: [missing-commit-probe]\n"+tc.mode),0600))
   out, stderr, code := runCLI(t,se,"hook","pre-commit")
   require.Equal(t,tc.code,code,stderr)
   require.Empty(t,out)
   require.Contains(t,stderr,"commit-probe")
  })
 }
 t.Run("broken policy blocks",func(t *testing.T) {
  se:=enabledHookRepo(t)
  require.NoError(t,os.WriteFile(filepath.Join(se.repo,".punt-labs/ethos.yaml"),[]byte("checks: absent\n"),0600))
  _,stderr,code:=runCLI(t,se,"hook","pre-commit")
  require.Equal(t,2,code,stderr)
  require.Contains(t,stderr,"absent")
 })
}
