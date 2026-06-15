package llirgen

import (
	"strings"
	"testing"

	"cedar-lang/internal/compiler"
	"cedar-lang/internal/lexer"
	"cedar-lang/internal/parser"
)

// generate runs the full front end + semantic analysis on src and returns the
// emitted LLVM IR as a string, failing the test on any error along the way.
func generate(t *testing.T, src string) string {
	t.Helper()

	p := parser.New(lexer.New(src))
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) != 0 {
		t.Fatalf("parser errors: %v", errs)
	}
	if _, err := compiler.NewWithState().Compile(program); err != nil {
		t.Fatalf("semantic analysis failed: %v", err)
	}
	module, err := Generate(program)
	if err != nil {
		t.Fatalf("codegen failed: %v", err)
	}
	return module.String()
}

func TestGeneratesMain(t *testing.T) {
	src := `program demo is
variable a : integer;
begin
a := 3;
end program.`

	ir := generate(t, src)
	if !strings.Contains(ir, "define i32 @main()") {
		t.Fatalf("expected a main function in:\n%s", ir)
	}
}

func TestGeneratesProcedureAndCall(t *testing.T) {
	src := `program demo is
variable r : integer;
procedure add : integer(variable x : integer)
begin
return x + 1;
end procedure;
begin
r := add(41);
end program.`

	ir := generate(t, src)
	for _, want := range []string{"define i32 @add(", "call i32 @add("} {
		if !strings.Contains(ir, want) {
			t.Errorf("expected IR to contain %q, got:\n%s", want, ir)
		}
	}
}

func TestNestedProceduresAreMangled(t *testing.T) {
	src := `program demo is
variable r : integer;
procedure outer : integer(variable x : integer)
procedure inner : integer(variable y : integer)
begin
return y + 1;
end procedure;
begin
return inner(x);
end procedure;
begin
r := outer(1);
end program.`

	ir := generate(t, src)
	// Nested procedures must get distinct, scope-qualified names.
	for _, want := range []string{"define i32 @outer(", "define i32 @outer.inner("} {
		if !strings.Contains(ir, want) {
			t.Errorf("expected IR to contain %q, got:\n%s", want, ir)
		}
	}
}

func TestControlFlowAndBuiltins(t *testing.T) {
	src := `program demo is
global variable x : integer;
variable i : integer;
variable out : bool;
begin
for(i := 0; i < 3)
if(i == 1) then
out := putInteger(i);
end if;
i := i + 1;
end for;
end program.`

	ir := generate(t, src)
	for _, want := range []string{"icmp", "br ", "call i32 (i8*, ...) @printf"} {
		if !strings.Contains(ir, want) {
			t.Errorf("expected IR to contain %q, got:\n%s", want, ir)
		}
	}
}
