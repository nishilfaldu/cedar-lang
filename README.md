# Cedar

[![Go](https://img.shields.io/badge/Go-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![LLVM](https://img.shields.io/badge/LLVM%20IR-262D3A?style=flat-square&logo=llvm&logoColor=white)](https://llvm.org/)
[![Built from scratch](https://img.shields.io/badge/built-from%20scratch-CC6699?style=flat-square)](#what-this-was)

> Cedar is a small statically-typed programming language and its compiler,
> written in Go. It was my first compiler, my first time writing Go, and my
> first encounter with lexers, parsers, and abstract syntax trees — all at once.
> It lowers source all the way to LLVM IR, so a Cedar program compiles to a
> real native executable.

## What this was

I built this before the AI-assisted era — no Cursor, no Copilot, and ChatGPT
was new enough that it could not write working code for something like this. So
I did it the only way available: from scratch, reading and figuring things out
as I went.

Two things were new to me at the same time. I had never written a line of Go,
and I picked it for this project purely to learn it. And I had never built a
compiler — I genuinely did not know what an AST was, what a token stream was, or
how a parser turned one into the other. I learned all of it on the way: lexing,
Pratt-style expression parsing, building symbol tables and scopes, type
checking, and wiring up LLVM IR generation with the `llir/llvm` library.

It remains one of my favorite things I have built, mostly because of how much I
did not know when I started.

## The language

Cedar is statically typed and case-insensitive (Pascal/Ada flavored). A program
is a header, a block of declarations, and a block of statements:

```text
program RecursiveFib is

global variable x : integer;
variable i : integer;
variable max : integer;
variable out : bool;

procedure Fib : integer(variable val : integer)

    procedure Sub : integer(variable val1 : integer)
    begin
        return val1 - 1;
    end procedure;

begin
    if (val == 0) then
        return 0;
    end if;
    if (val == 1) then
        return 1;
    end if;
    return val + Fib(Sub(val));
end procedure;

begin
    max := getInteger();
    for (i := 0; i < max)
        x := Fib(i);
        out := putInteger(x);
        i := i + 1;
    end for;
end program.
```

(This one shows off nested procedures and recursion — `Fib` declares `Sub`
inside its own scope.)

Features it supports:

| Area | Details |
|---|---|
| Types | `integer`, `float`, `string`, `bool`, and fixed-size arrays |
| Declarations | local and `global` variables; nested procedures |
| Control flow | `if / else / end if`, `for ( init ; condition ) ... end for` |
| Procedures | typed parameters, return values, recursion |
| Operators | arithmetic, comparison, logical (`&`, `|`, `not`) with precedence |
| Built-in I/O | `getInteger` / `putInteger`, and the same for `float`, `string`, `bool` |

The full grammar lives in [`internal/grammar/cedar.g4`](internal/grammar/cedar.g4).

## How the compiler is built

```text
source (.src)
     |
     v   lexer        internal/lexer     hand-written, case-insensitive scanner
   tokens
     |
     v   parser       internal/parser    Pratt-style expression parsing -> AST
    AST
     |
     v   analyzer     internal/compiler  symbol tables, scopes, type checking
 typed AST
     |
     v   back end     internal/llirgen   LLVM IR via llir/llvm
  LLVM IR
     |
     v   clang
  native executable
```

The whole pipeline runs end to end: source is scanned into tokens, parsed into
an AST, checked for scope and type errors, and then lowered to LLVM IR in
`internal/llirgen`. Handing that IR to `clang` produces a native binary, so a
Cedar program actually runs.

## Project layout

```text
main.go                 entry point (starts the REPL)
internal/
  token/                token definitions
  lexer/                source -> tokens
  ast/                  AST node types
  parser/               tokens -> AST
  object/               runtime object + built-in function definitions
  compiler/             semantic analysis: symbol tables, scopes, type checks
  llirgen/              LLVM IR generation helpers (llir/llvm)
  repl/                 interactive read-eval-print loop
  util/                 batch runner over a directory of .src files
  grammar/cedar.g4      the language grammar
tests/
  correct/              sample programs expected to compile
  incorrect/            sample programs expected to be rejected
```

## Running it

Requires Go 1.21+. Compiling a program to a native binary also needs `clang`
(or any tool that can assemble LLVM IR).

Compile a Cedar program to an executable:

```bash
go run . build tests/correct/math.src   # writes tests/correct/math.ll
clang math.ll -o math                    # IR -> native binary
./math                                   # prints 610  (Fib(15))
```

`build -o out.ll` chooses the output path, and `build -o -` prints the IR to
stdout. Programs that read input use the `getInteger` / `getString` builtins:

```bash
go run . build tests/correct/recursiveFib.src -o fib.ll
clang fib.ll -o fib
echo 8 | ./fib        # prints Fib(0)..Fib(7)
```

Other useful commands:

```bash
go run .          # start the REPL and type Cedar at the >> prompt
go test ./...     # run the lexer, parser, and codegen tests
go build ./...    # compile everything
```

Sample programs to read or feed in live under [`tests/`](tests).
