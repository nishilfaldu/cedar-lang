package main

import (
	"cedar-lang/internal/compiler"
	"cedar-lang/internal/lexer"
	"cedar-lang/internal/llirgen"
	"cedar-lang/internal/parser"
	"cedar-lang/internal/repl"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

func main() {
	// `cedar build <file.src>` lowers a program to LLVM IR.
	if len(os.Args) >= 2 && os.Args[1] == "build" {
		if err := build(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// No subcommand: start the interactive REPL.
	u, err := user.Current()
	if err != nil {
		panic(err)
	}
	fmt.Printf("Hello %s! This is the Cedar programming language!\n", u.Username)
	repl.Start(os.Stdin, os.Stdout)
}

// build compiles a single .src file to LLVM IR. It writes a sibling .ll file
// next to the source unless -o is given (use "-" to write to stdout).
func build(args []string) error {
	var srcPath, outPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o":
			if i+1 >= len(args) {
				return fmt.Errorf("build: -o requires a path")
			}
			outPath = args[i+1]
			i++
		default:
			srcPath = args[i]
		}
	}
	if srcPath == "" {
		return fmt.Errorf("usage: cedar build [-o out.ll|-] <file.src>")
	}

	code, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	p := parser.New(lexer.New(string(code)))
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) != 0 {
		return fmt.Errorf("parser errors:\n\t%s", strings.Join(errs, "\n\t"))
	}

	// Semantic analysis must pass before we lower to IR.
	if _, err := compiler.NewWithState().Compile(program); err != nil {
		return fmt.Errorf("compilation failed: %w", err)
	}

	module, err := llirgen.Generate(program)
	if err != nil {
		return err
	}
	ir := module.String()

	if outPath == "-" {
		fmt.Print(ir)
		return nil
	}
	if outPath == "" {
		outPath = strings.TrimSuffix(srcPath, filepath.Ext(srcPath)) + ".ll"
	}
	if err := os.WriteFile(outPath, []byte(ir), 0o644); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	fmt.Printf("wrote %s\n", outPath)
	return nil
}
