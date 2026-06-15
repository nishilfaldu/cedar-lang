// Package llirgen holds the LLVM IR generation helpers built on top of the
// llir/llvm library. This is where Cedar's back end was headed: turning the
// type-checked AST into LLVM IR. The front end and semantic analysis are
// wired up end to end; IR emission is still being grown out from helpers
// like the one below.
package llirgen

import (
	"github.com/llir/llvm/ir"
	"github.com/llir/llvm/ir/constant"
	"github.com/llir/llvm/ir/types"
)

// LLVMIRGlobalVariable defines a 64-bit integer global in the given module.
func LLVMIRGlobalVariable(m *ir.Module, name string, value int64) *ir.Global {
	return m.NewGlobalDef(name, constant.NewInt(types.I64, value))
}
