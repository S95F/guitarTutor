// Command guitartutor is the entry point for the guitarTutor application.
//
// The project is in its planning phase; see README.md and ROADMAP.md at the
// repository root for where this is headed.
package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "0.0.1-dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("guitartutor", version)
		return
	}

	fmt.Fprintln(os.Stderr, "guitartutor is not implemented yet — see ROADMAP.md")
	os.Exit(1)
}
