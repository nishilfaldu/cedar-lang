package main

import (
	"cedar-lang/internal/repl"
	"fmt"
	"os"
	"os/user"
)

func main() {
	// get the current user
	user, err := user.Current()
	if err != nil {
		panic(err)
	}
	// print a welcome message
	fmt.Printf("Hello %s! This is the Cedar programming language!\n", user.Username)
	// start the REPL
	repl.Start(os.Stdin, os.Stdout)
}
