package parser

import (
	"testing"

	"cedar-lang/internal/ast"
	"cedar-lang/internal/lexer"
)

// parse is a small helper that runs the lexer + parser over a source string
// and fails the test if the parser reported any errors.
func parse(t *testing.T, input string) *ast.Program {
	t.Helper()

	p := New(lexer.New(input))
	program := p.ParseProgram()

	if errs := p.Errors(); len(errs) != 0 {
		t.Fatalf("parser had %d error(s):\n%v", len(errs), errs)
	}
	if program == nil {
		t.Fatal("ParseProgram() returned nil")
	}

	return program
}

func TestProgramHeader(t *testing.T) {
	program := parse(t, `program Demo is
begin
end program.`)

	if program.Header == nil {
		t.Fatal("program.Header is nil")
	}
	// Cedar is case-insensitive: the lexer folds source to lowercase, so the
	// identifier "Demo" is parsed as "demo".
	if got := program.Header.Identifier.Value; got != "demo" {
		t.Errorf("header identifier = %q, want %q", got, "demo")
	}
}

func TestVariableDeclarations(t *testing.T) {
	program := parse(t, `program Demo is
global variable count : integer;
variable name : string;
begin
end program.`)

	decls := program.Body.Declarations
	if len(decls) != 2 {
		t.Fatalf("got %d declarations, want 2", len(decls))
	}

	global, ok := decls[0].(*ast.GlobalVariableDeclaration)
	if !ok {
		t.Fatalf("decls[0] is %T, want *ast.GlobalVariableDeclaration", decls[0])
	}
	if name := global.VariableDeclaration.Name.Value; name != "count" {
		t.Errorf("global var name = %q, want %q", name, "count")
	}
	if typ := global.VariableDeclaration.Type.Name; typ != "integer" {
		t.Errorf("global var type = %q, want %q", typ, "integer")
	}

	local, ok := decls[1].(*ast.VariableDeclaration)
	if !ok {
		t.Fatalf("decls[1] is %T, want *ast.VariableDeclaration", decls[1])
	}
	if name := local.Name.Value; name != "name" {
		t.Errorf("local var name = %q, want %q", name, "name")
	}
	if typ := local.Type.Name; typ != "string" {
		t.Errorf("local var type = %q, want %q", typ, "string")
	}
}

func TestAssignmentStatement(t *testing.T) {
	program := parse(t, `program Demo is
variable x : integer;
begin
x := 5;
end program.`)

	stmts := program.Body.Statements
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}

	assign, ok := stmts[0].(*ast.AssignmentStatement)
	if !ok {
		t.Fatalf("stmts[0] is %T, want *ast.AssignmentStatement", stmts[0])
	}
	if dest := assign.Destination.Identifier.Value; dest != "x" {
		t.Errorf("assignment destination = %q, want %q", dest, "x")
	}
	if assign.Value == nil {
		t.Error("assignment value is nil")
	}
}
