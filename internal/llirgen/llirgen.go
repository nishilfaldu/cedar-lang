// Package llirgen is Cedar's LLVM IR back end. It walks the type-checked AST
// produced by the front end and lowers it into an LLVM IR module using the
// llir/llvm library. The result can be handed to clang to produce a native
// executable, so the pipeline runs end to end:
//
//	source -> tokens -> AST -> semantic analysis -> LLVM IR -> native binary
//
// The pass is self-contained: rather than reusing the semantic analyzer's
// symbol table (which is tuned for type checking), it keeps its own small
// scope model mapping names to storage (allocas / globals) and procedures to
// their generated functions.
package llirgen

import (
	"fmt"

	"cedar-lang/internal/ast"

	"github.com/llir/llvm/ir"
	"github.com/llir/llvm/ir/constant"
	"github.com/llir/llvm/ir/enum"
	"github.com/llir/llvm/ir/types"
	"github.com/llir/llvm/ir/value"
)

// LLVMIRGlobalVariable defines a 64-bit integer global in the given module.
// It is a small standalone helper kept from the early days of the back end.
func LLVMIRGlobalVariable(m *ir.Module, name string, value int64) *ir.Global {
	return m.NewGlobalDef(name, constant.NewInt(types.I64, value))
}

// binding is a storage slot for a variable: a pointer (alloca or global) plus
// the Cedar type it holds. Arrays additionally track their element type/length.
type binding struct {
	ptr     value.Value
	ctype   string // "integer" | "float" | "bool" | "string"
	isArray bool
	arrLen  int64
	elemLL  types.Type // llvm element type (scalar type, or array element type)
}

// procEntry is a generated procedure: its AST, its LLVM function, the mangled
// name, return type, params, and the procedure scope visible inside its body.
type procEntry struct {
	decl       *ast.ProcedureDeclaration
	fn         *ir.Func
	mangled    string
	retType    string
	params     []*ast.VariableDeclaration
	childScope *procScope
}

// procScope is a lexical scope of procedures. Lookup walks outward through
// parents, so inner declarations shadow outer ones (matching Cedar semantics).
type procScope struct {
	table  map[string]*procEntry
	parent *procScope
}

func (s *procScope) resolve(name string) (*procEntry, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if e, ok := cur.table[name]; ok {
			return e, true
		}
	}
	return nil, false
}

// tval is a generated value together with its Cedar type.
type tval struct {
	val   value.Value
	ctype string
}

// Generator lowers a type-checked Program into an LLVM IR module.
type Generator struct {
	m *ir.Module

	// runtime declarations
	printf *ir.Func
	scanf  *ir.Func
	strcmp *ir.Func
	sqrtFn *ir.Func

	globals map[string]binding    // program-level `global` variables
	strs    map[string]*ir.Global // interned string literals + format strings

	// per-function state, reset for each function we generate
	fn      *ir.Func
	cur     *ir.Block
	locals  map[string]binding
	procs   *procScope
	retType string
}

// Generate builds and returns an LLVM IR module for the given program. The
// program is expected to have already passed semantic analysis.
func Generate(program *ast.Program) (*ir.Module, error) {
	g := &Generator{
		m:       ir.NewModule(),
		globals: map[string]binding{},
		strs:    map[string]*ir.Global{},
	}
	return g.run(program)
}

func (g *Generator) run(program *ast.Program) (m *ir.Module, err error) {
	// Lowering uses panics internally for unsupported constructs; turn the
	// outermost one into an error so callers get a clean failure.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("codegen: %v", r)
		}
	}()

	g.declareRuntime()

	if program.Body == nil {
		return nil, fmt.Errorf("program has no body")
	}

	// Program-level `global` variables become LLVM globals (procedures may
	// read them). Plain program-level variables live in main's frame.
	for _, decl := range program.Body.Declarations {
		if gv, ok := decl.(*ast.GlobalVariableDeclaration); ok {
			g.declareGlobal(gv.VariableDeclaration)
		}
	}

	// Declare every procedure (recursively) before emitting any body, so
	// calls — including forward references and recursion — resolve.
	root := g.declareProcs(program.Body.Declarations, nil, "")

	g.genMain(program.Body, root)
	g.genProcBodies(program.Body.Declarations, root)

	return g.m, nil
}

// declareRuntime declares the C/LLVM functions the generated code calls into.
func (g *Generator) declareRuntime() {
	i8ptr := types.NewPointer(types.I8)

	g.printf = g.m.NewFunc("printf", types.I32, ir.NewParam("", i8ptr))
	g.printf.Sig.Variadic = true

	g.scanf = g.m.NewFunc("scanf", types.I32, ir.NewParam("", i8ptr))
	g.scanf.Sig.Variadic = true

	g.strcmp = g.m.NewFunc("strcmp", types.I32,
		ir.NewParam("", i8ptr), ir.NewParam("", i8ptr))

	// llvm.sqrt.f64 intrinsic — no extra linking required.
	g.sqrtFn = g.m.NewFunc("llvm.sqrt.f64", types.Double, ir.NewParam("", types.Double))
}

// declareProcs creates LLVM functions for the procedures declared at one level
// and recursively for their nested procedures, returning the scope that holds
// them. prefix is the mangled-name prefix accumulated from enclosing scopes.
func (g *Generator) declareProcs(decls []ast.Declaration, parent *procScope, prefix string) *procScope {
	s := &procScope{table: map[string]*procEntry{}, parent: parent}

	for _, d := range decls {
		pd, ok := d.(*ast.ProcedureDeclaration)
		if !ok {
			continue
		}
		name := pd.Header.Name.Value
		mangled := prefix + name

		params := make([]*ir.Param, 0, len(pd.Header.Parameters))
		for _, p := range pd.Header.Parameters {
			params = append(params, ir.NewParam(p.Name.Value, g.llType(p.Type.Name)))
		}
		fn := g.m.NewFunc(mangled, g.llType(pd.Header.TypeMark.Name), params...)

		s.table[name] = &procEntry{
			decl:    pd,
			fn:      fn,
			mangled: mangled,
			retType: pd.Header.TypeMark.Name,
			params:  pd.Header.Parameters,
		}
	}

	// Build child scopes once all siblings exist (so they can see each other).
	for _, d := range decls {
		pd, ok := d.(*ast.ProcedureDeclaration)
		if !ok {
			continue
		}
		e := s.table[pd.Header.Name.Value]
		e.childScope = g.declareProcs(pd.Body.Declarations, s, e.mangled+".")
	}

	return s
}

// genProcBodies emits the body of every procedure at one level and recurses.
func (g *Generator) genProcBodies(decls []ast.Declaration, scope *procScope) {
	for _, d := range decls {
		pd, ok := d.(*ast.ProcedureDeclaration)
		if !ok {
			continue
		}
		e := scope.table[pd.Header.Name.Value]
		g.genProc(e)
		g.genProcBodies(pd.Body.Declarations, e.childScope)
	}
}

// genMain emits the LLVM `main` function from the program body.
func (g *Generator) genMain(body *ast.ProgramBody, scope *procScope) {
	g.fn = g.m.NewFunc("main", types.I32)
	g.cur = g.fn.NewBlock("entry")
	g.locals = map[string]binding{}
	g.procs = scope
	g.retType = "integer"

	// Plain (non-global) program-level variables are main's locals.
	for _, decl := range body.Declarations {
		if vd, ok := decl.(*ast.VariableDeclaration); ok {
			g.declareLocal(vd)
		}
	}

	for _, stmt := range body.Statements {
		g.genStmt(stmt)
	}

	if g.cur.Term == nil {
		g.cur.NewRet(constant.NewInt(types.I32, 0))
	}
}

// genProc emits the body of a single procedure.
func (g *Generator) genProc(e *procEntry) {
	g.fn = e.fn
	g.cur = e.fn.NewBlock("entry")
	g.locals = map[string]binding{}
	g.procs = e.childScope
	g.retType = e.retType

	// Spill parameters into allocas so they can be reassigned like locals.
	for i, p := range e.params {
		llt := g.llType(p.Type.Name)
		ptr := g.cur.NewAlloca(llt)
		g.cur.NewStore(e.fn.Params[i], ptr)
		g.locals[p.Name.Value] = binding{ptr: ptr, ctype: p.Type.Name, elemLL: llt}
	}

	for _, decl := range e.decl.Body.Declarations {
		if vd, ok := decl.(*ast.VariableDeclaration); ok {
			g.declareLocal(vd)
		}
	}

	for _, stmt := range e.decl.Body.Statements {
		g.genStmt(stmt)
	}

	// Guarantee every path is terminated (fallthrough returns a zero value).
	if g.cur.Term == nil {
		g.cur.NewRet(g.zeroValue(e.retType))
	}
}

// ---- declarations ---------------------------------------------------------

func (g *Generator) declareGlobal(vd *ast.VariableDeclaration) {
	if vd.Type.Array != nil {
		elem := g.llType(vd.Type.Name)
		n := uint64(vd.Type.Array.Value)
		arrT := types.NewArray(n, elem)
		gv := g.m.NewGlobalDef(vd.Name.Value, constant.NewZeroInitializer(arrT))
		g.globals[vd.Name.Value] = binding{
			ptr: gv, ctype: vd.Type.Name + "[]", isArray: true,
			arrLen: vd.Type.Array.Value, elemLL: elem,
		}
		return
	}
	llt := g.llType(vd.Type.Name)
	gv := g.m.NewGlobalDef(vd.Name.Value, g.zeroValue(vd.Type.Name))
	g.globals[vd.Name.Value] = binding{ptr: gv, ctype: vd.Type.Name, elemLL: llt}
}

func (g *Generator) declareLocal(vd *ast.VariableDeclaration) {
	if vd.Type.Array != nil {
		elem := g.llType(vd.Type.Name)
		n := uint64(vd.Type.Array.Value)
		arrT := types.NewArray(n, elem)
		ptr := g.cur.NewAlloca(arrT)
		g.locals[vd.Name.Value] = binding{
			ptr: ptr, ctype: vd.Type.Name + "[]", isArray: true,
			arrLen: vd.Type.Array.Value, elemLL: elem,
		}
		return
	}
	llt := g.llType(vd.Type.Name)
	ptr := g.cur.NewAlloca(llt)
	g.locals[vd.Name.Value] = binding{ptr: ptr, ctype: vd.Type.Name, elemLL: llt}
}

func (g *Generator) lookup(name string) (binding, bool) {
	if b, ok := g.locals[name]; ok {
		return b, true
	}
	if b, ok := g.globals[name]; ok {
		return b, true
	}
	return binding{}, false
}

// ---- statements -----------------------------------------------------------

func (g *Generator) genStmt(stmt ast.Statement) {
	switch s := stmt.(type) {
	case *ast.AssignmentStatement:
		g.genAssign(s)
	case *ast.ReturnStatement:
		v := g.genExpr(s.ReturnValue)
		g.cur.NewRet(g.cast(v, g.retType))
	case *ast.LoopStatement:
		g.genFor(s)
	case *ast.ExpressionStatement:
		if ie, ok := s.Expression.(*ast.IfExpression); ok {
			g.genIf(ie)
		} else {
			g.genExpr(s.Expression) // e.g. a bare call; value discarded
		}
	default:
		panic(fmt.Sprintf("unsupported statement %T", stmt))
	}
}

func (g *Generator) genAssign(s *ast.AssignmentStatement) {
	rhs := g.genExpr(s.Value)
	b, ok := g.lookup(s.Destination.Identifier.Value)
	if !ok {
		panic(fmt.Sprintf("undefined variable %s", s.Destination.Identifier.Value))
	}

	if s.Destination.Expression != nil {
		// array element assignment: dst[idx] := rhs
		idx := g.cast(g.genExpr(s.Destination.Expression), "integer")
		elemPtr := g.elemPtr(b, idx)
		g.cur.NewStore(g.cast(rhs, b.ctype), elemPtr)
		return
	}
	g.cur.NewStore(g.cast(rhs, b.ctype), b.ptr)
}

func (g *Generator) genIf(ie *ast.IfExpression) {
	cond := g.toBool(g.genExpr(ie.Condition))

	thenB := g.fn.NewBlock("")
	mergeB := g.fn.NewBlock("")
	elseB := mergeB
	if ie.Alternative != nil {
		elseB = g.fn.NewBlock("")
	}

	g.cur.NewCondBr(cond, thenB, elseB)

	g.cur = thenB
	for _, st := range ie.Consequence.Statements {
		g.genStmt(st)
	}
	if g.cur.Term == nil {
		g.cur.NewBr(mergeB)
	}

	if ie.Alternative != nil {
		g.cur = elseB
		for _, st := range ie.Alternative.Statements {
			g.genStmt(st)
		}
		if g.cur.Term == nil {
			g.cur.NewBr(mergeB)
		}
	}

	g.cur = mergeB
}

func (g *Generator) genFor(ls *ast.LoopStatement) {
	if ls.InitStatement != nil {
		g.genAssign(ls.InitStatement)
	}

	condB := g.fn.NewBlock("")
	bodyB := g.fn.NewBlock("")
	afterB := g.fn.NewBlock("")

	g.cur.NewBr(condB)

	g.cur = condB
	cond := g.toBool(g.genExpr(ls.Condition))
	g.cur.NewCondBr(cond, bodyB, afterB)

	g.cur = bodyB
	for _, st := range ls.Body.Statements {
		g.genStmt(st)
	}
	if g.cur.Term == nil {
		g.cur.NewBr(condB)
	}

	g.cur = afterB
}

// ---- expressions ----------------------------------------------------------

func (g *Generator) genExpr(expr ast.Expression) tval {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return tval{constant.NewInt(types.I32, e.Value), "integer"}
	case *ast.FloatLiteral:
		return tval{constant.NewFloat(types.Double, e.Value), "float"}
	case *ast.Boolean:
		return tval{constant.NewBool(e.Value), "bool"}
	case *ast.StringLiteral:
		return tval{g.globalStr(e.Value), "string"}
	case *ast.Identifier:
		b, ok := g.lookup(e.Value)
		if !ok {
			panic(fmt.Sprintf("undefined variable %s", e.Value))
		}
		return tval{g.cur.NewLoad(b.elemLL, b.ptr), b.ctype}
	case *ast.IndexExpression:
		name := e.Left.String()
		b, ok := g.lookup(name)
		if !ok {
			panic(fmt.Sprintf("undefined array %s", name))
		}
		idx := g.cast(g.genExpr(e.Index), "integer")
		ptr := g.elemPtr(b, idx)
		return tval{g.cur.NewLoad(b.elemLL, ptr), b.ctype[:len(b.ctype)-2]}
	case *ast.PrefixExpression:
		return g.genPrefix(e)
	case *ast.InfixExpression:
		return g.genInfix(e)
	case *ast.CallExpression:
		return g.genCall(e)
	default:
		panic(fmt.Sprintf("unsupported expression %T", expr))
	}
}

func (g *Generator) genPrefix(e *ast.PrefixExpression) tval {
	r := g.genExpr(e.Right)
	switch e.Operator {
	case "-":
		if r.ctype == "float" {
			return tval{g.cur.NewFNeg(r.val), "float"}
		}
		return tval{g.cur.NewSub(constant.NewInt(types.I32, 0), g.cast(r, "integer")), "integer"}
	case "not":
		return tval{g.cur.NewXor(g.toBool(r), constant.NewBool(true)), "bool"}
	default:
		panic(fmt.Sprintf("unsupported prefix operator %q", e.Operator))
	}
}

func (g *Generator) genInfix(e *ast.InfixExpression) tval {
	switch e.Operator {
	case "&", "|":
		l := g.toBool(g.genExpr(e.Left))
		r := g.toBool(g.genExpr(e.Right))
		if e.Operator == "&" {
			return tval{g.cur.NewAnd(l, r), "bool"}
		}
		return tval{g.cur.NewOr(l, r), "bool"}
	}

	lhs := g.genExpr(e.Left)
	rhs := g.genExpr(e.Right)

	// String comparison lowers to strcmp(lhs, rhs) <pred> 0.
	if lhs.ctype == "string" || rhs.ctype == "string" {
		cmp := g.cur.NewCall(g.strcmp, lhs.val, rhs.val)
		return tval{g.cur.NewICmp(intPred(e.Operator), cmp, constant.NewInt(types.I32, 0)), "bool"}
	}

	isFloat := lhs.ctype == "float" || rhs.ctype == "float"

	switch e.Operator {
	case "+", "-", "*", "/":
		if isFloat {
			l, r := g.cast(lhs, "float"), g.cast(rhs, "float")
			switch e.Operator {
			case "+":
				return tval{g.cur.NewFAdd(l, r), "float"}
			case "-":
				return tval{g.cur.NewFSub(l, r), "float"}
			case "*":
				return tval{g.cur.NewFMul(l, r), "float"}
			default:
				return tval{g.cur.NewFDiv(l, r), "float"}
			}
		}
		l, r := g.cast(lhs, "integer"), g.cast(rhs, "integer")
		switch e.Operator {
		case "+":
			return tval{g.cur.NewAdd(l, r), "integer"}
		case "-":
			return tval{g.cur.NewSub(l, r), "integer"}
		case "*":
			return tval{g.cur.NewMul(l, r), "integer"}
		default:
			return tval{g.cur.NewSDiv(l, r), "integer"}
		}
	case "<", "<=", ">", ">=", "==", "!=":
		if isFloat {
			l, r := g.cast(lhs, "float"), g.cast(rhs, "float")
			return tval{g.cur.NewFCmp(floatPred(e.Operator), l, r), "bool"}
		}
		l, r := g.cast(lhs, "integer"), g.cast(rhs, "integer")
		return tval{g.cur.NewICmp(intPred(e.Operator), l, r), "bool"}
	default:
		panic(fmt.Sprintf("unsupported infix operator %q", e.Operator))
	}
}

func (g *Generator) genCall(e *ast.CallExpression) tval {
	name := e.Function.String()
	if g.isBuiltin(name) {
		return g.genBuiltin(name, e.Arguments)
	}

	proc, ok := g.procs.resolve(name)
	if !ok {
		panic(fmt.Sprintf("undefined function %s", name))
	}
	args := make([]value.Value, len(e.Arguments))
	for i, a := range e.Arguments {
		want := proc.params[i].Type.Name
		args[i] = g.cast(g.genExpr(a), want)
	}
	return tval{g.cur.NewCall(proc.fn, args...), proc.retType}
}

// ---- built-in I/O ---------------------------------------------------------

func (g *Generator) isBuiltin(name string) bool {
	switch name {
	case "putinteger", "putfloat", "putstring", "putbool",
		"getinteger", "getfloat", "getstring", "getbool", "sqrt":
		return true
	}
	return false
}

func (g *Generator) genBuiltin(name string, args []ast.Expression) tval {
	switch name {
	case "putinteger":
		v := g.cast(g.genExpr(args[0]), "integer")
		g.cur.NewCall(g.printf, g.globalStr("%d\n"), v)
		return tval{constant.NewBool(true), "bool"}
	case "putbool":
		v := g.cur.NewZExt(g.toBool(g.genExpr(args[0])), types.I32)
		g.cur.NewCall(g.printf, g.globalStr("%d\n"), v)
		return tval{constant.NewBool(true), "bool"}
	case "putfloat":
		v := g.cast(g.genExpr(args[0]), "float")
		g.cur.NewCall(g.printf, g.globalStr("%f\n"), v)
		return tval{constant.NewBool(true), "bool"}
	case "putstring":
		v := g.genExpr(args[0])
		g.cur.NewCall(g.printf, g.globalStr("%s\n"), v.val)
		return tval{constant.NewBool(true), "bool"}
	case "getinteger":
		tmp := g.cur.NewAlloca(types.I32)
		g.cur.NewCall(g.scanf, g.globalStr("%d"), tmp)
		return tval{g.cur.NewLoad(types.I32, tmp), "integer"}
	case "getfloat":
		tmp := g.cur.NewAlloca(types.Double)
		g.cur.NewCall(g.scanf, g.globalStr("%lf"), tmp)
		return tval{g.cur.NewLoad(types.Double, tmp), "float"}
	case "getbool":
		tmp := g.cur.NewAlloca(types.I32)
		g.cur.NewCall(g.scanf, g.globalStr("%d"), tmp)
		loaded := g.cur.NewLoad(types.I32, tmp)
		return tval{g.cur.NewICmp(enum.IPredNE, loaded, constant.NewInt(types.I32, 0)), "bool"}
	case "getstring":
		buf := g.cur.NewAlloca(types.NewArray(256, types.I8))
		ptr := g.cur.NewGetElementPtr(types.NewArray(256, types.I8), buf,
			constant.NewInt(types.I64, 0), constant.NewInt(types.I64, 0))
		g.cur.NewCall(g.scanf, g.globalStr("%255s"), ptr)
		return tval{ptr, "string"}
	case "sqrt":
		v := g.cast(g.genExpr(args[0]), "float")
		return tval{g.cur.NewCall(g.sqrtFn, v), "float"}
	default:
		panic(fmt.Sprintf("unknown builtin %s", name))
	}
}

// ---- helpers --------------------------------------------------------------

func (g *Generator) llType(ct string) types.Type {
	switch ct {
	case "integer":
		return types.I32
	case "float":
		return types.Double
	case "bool":
		return types.I1
	case "string":
		return types.NewPointer(types.I8)
	default:
		panic(fmt.Sprintf("unknown type %q", ct))
	}
}

func (g *Generator) zeroValue(ct string) constant.Constant {
	switch ct {
	case "integer":
		return constant.NewInt(types.I32, 0)
	case "float":
		return constant.NewFloat(types.Double, 0)
	case "bool":
		return constant.NewBool(false)
	case "string":
		return constant.NewNull(types.NewPointer(types.I8))
	default:
		panic(fmt.Sprintf("unknown type %q", ct))
	}
}

// elemPtr returns a pointer to element idx of array binding b.
func (g *Generator) elemPtr(b binding, idx value.Value) value.Value {
	arrT := types.NewArray(uint64(b.arrLen), b.elemLL)
	return g.cur.NewGetElementPtr(arrT, b.ptr, constant.NewInt(types.I64, 0), idx)
}

// cast converts a value to the requested Cedar type, inserting the appropriate
// conversion instruction when the source and target differ. Cedar's loose
// numeric/bool typing means these casts paper over the analyzer's leniency.
func (g *Generator) cast(v tval, target string) value.Value {
	if v.ctype == target {
		return v.val
	}
	switch {
	case v.ctype == "integer" && target == "float":
		return g.cur.NewSIToFP(v.val, types.Double)
	case v.ctype == "float" && target == "integer":
		return g.cur.NewFPToSI(v.val, types.I32)
	case v.ctype == "bool" && target == "integer":
		return g.cur.NewZExt(v.val, types.I32)
	case v.ctype == "integer" && target == "bool":
		return g.cur.NewICmp(enum.IPredNE, v.val, constant.NewInt(types.I32, 0))
	case v.ctype == "bool" && target == "float":
		return g.cur.NewSIToFP(g.cur.NewZExt(v.val, types.I32), types.Double)
	default:
		// No known conversion (e.g. string<->string handled above); pass through.
		return v.val
	}
}

func (g *Generator) toBool(v tval) value.Value {
	switch v.ctype {
	case "bool":
		return v.val
	case "integer":
		return g.cur.NewICmp(enum.IPredNE, v.val, constant.NewInt(types.I32, 0))
	case "float":
		return g.cur.NewFCmp(enum.FPredONE, v.val, constant.NewFloat(types.Double, 0))
	default:
		panic(fmt.Sprintf("cannot use %s as a boolean", v.ctype))
	}
}

// globalStr interns a string and returns an i8* pointing at its first byte.
func (g *Generator) globalStr(s string) value.Value {
	gv, ok := g.strs[s]
	if !ok {
		ca := constant.NewCharArrayFromString(s + "\x00")
		gv = g.m.NewGlobalDef(fmt.Sprintf(".str.%d", len(g.strs)), ca)
		gv.Immutable = true
		g.strs[s] = gv
	}
	zero := constant.NewInt(types.I64, 0)
	return g.cur.NewGetElementPtr(gv.ContentType, gv, zero, zero)
}

func intPred(op string) enum.IPred {
	switch op {
	case "==":
		return enum.IPredEQ
	case "!=":
		return enum.IPredNE
	case "<":
		return enum.IPredSLT
	case "<=":
		return enum.IPredSLE
	case ">":
		return enum.IPredSGT
	case ">=":
		return enum.IPredSGE
	default:
		panic(fmt.Sprintf("not a comparison operator %q", op))
	}
}

func floatPred(op string) enum.FPred {
	switch op {
	case "==":
		return enum.FPredOEQ
	case "!=":
		return enum.FPredONE
	case "<":
		return enum.FPredOLT
	case "<=":
		return enum.FPredOLE
	case ">":
		return enum.FPredOGT
	case ">=":
		return enum.FPredOGE
	default:
		panic(fmt.Sprintf("not a comparison operator %q", op))
	}
}
