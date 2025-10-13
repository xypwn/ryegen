// Example of the "no-main" option, which allows the user to provide a ProgramState and
// register the generated builtins themselves.
//
// A major use case would be using Rye as an embedded scripting language. In this case,
// you can define an entire host-script interface API in Go and let Ryegen handle the
// translation.
//
// - "bindings" directory: ryegen.toml and where the generated bindings end up
// - "api" directory: custom package with the functions we're generating bindings for
package main

import (
	"github.com/refaktor/rye/env"
	"github.com/refaktor/rye/evaldo"
	"github.com/refaktor/rye/runner"
	"github.com/refaktor/ryegen/v2/examples/no-main/bindings"
)

func main() {
	// We're using runner in this case, for simplicity. But you can
	// use this on any ProgramState.
	runner.DoMain(func(ps *env.ProgramState) error {
		// RegisterBaseBuiltins provides nil, is-nil etc.
		bindings.RegisterBaseBuiltins(ps)
		// Iterate through all builtins (map[<package name>]map[<builtin name>]<builtin>).
		for _, builtins := range bindings.Builtins() {
			// Registering to the "base" context makes the builtins
			// available globally.
			evaldo.RegisterVarBuiltins2(builtins, ps, "base")
		}
		return nil
	})
}
