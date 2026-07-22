package main

import "io"

// execCLI runs the full cobra tree the way a real invocation would: args
// are everything after "runko-ci". The returned error is what main() maps
// to an exit code (usageError -> 2, anything else -> 1). Command bodies
// print to os.Stdout directly; cobra's own help/usage rendering is
// discarded.
func execCLI(args ...string) error {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root.Execute()
}
