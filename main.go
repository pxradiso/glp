package main

import (
	"errors"
	"fmt"
	"os"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	maybeCheckUpdate(cmd)

	var err error
	switch cmd {
	case "search", "s":
		err = cmdSearch(args)
	case "info":
		err = cmdInfo(args)
	case "install", "i", "add":
		err = cmdInstall(args)
	case "list", "ls":
		err = cmdList(args)
	case "remove", "rm", "uninstall":
		err = cmdRemove(args)
	case "upgrade", "up":
		err = cmdUpgrade(args)
	case "doctor", "fix":
		err = cmdDoctor(args)
	case "index":
		err = cmdIndex(args)
	case "update":
		err = cmdUpdate(args)
	case "version", "--version", "-v":
		fmt.Printf("glp %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("unknown command %q (try `glp help`)", cmd)
	}
	if err != nil {
		var c *cancel
		if errors.As(err, &c) {
			fmt.Fprintln(os.Stderr)
			if c.hint != "" {
				fmt.Fprintln(os.Stderr, "  "+c.hint)
			}
			msg := c.msg
			if msg == "" {
				msg = "nothing changed"
			}
			fmt.Fprintln(os.Stderr, style("2", msg))
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "glp: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`glp - install desktop apps on any distro

  glp search <term>
  glp info <app>
  glp install <app>[@ver]
  glp list
  glp upgrade [app]
  glp remove <app>
  glp doctor [app]
  glp update [--check]
  glp index refresh
  glp index show
  glp index set <url>
  glp version
`)
}
