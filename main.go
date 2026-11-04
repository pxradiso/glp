package main

import (
	"errors"
	"fmt"
	"os"
)

var version = "0.1.2"

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
			fmt.Fprintln(os.Stderr, errStyle("2", msg))
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "glp: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Printf("\n%s\n", outStyle("1", "glp "+version))
	fmt.Printf("%s\n", outStyle("2", "install desktop apps easily from a signed catalog"))
	fmt.Printf("%s\n\n", outStyle("2", "usage: glp <command> [args]"))

	section := func(title string, rows [][]string) {
		fmt.Println(outStyle("1", title))
		writeTable(os.Stdout, "  ", nil, rows)
		fmt.Println()
	}

	section("Find apps", [][]string{
		{"search <term>", "search names, commands and summaries"},
		{"info <app>", "version, size, checksum and system packages"},
		{"list", "show installed apps"},
	})
	section("Install and manage", [][]string{
		{"install <app>[@ver]", "download, verify, unpack and add to the menu"},
		{"upgrade [app]", "update one app or all installed apps"},
		{"remove <app>", "remove launchers, menu entry, icon and files"},
		{"doctor [app]", "find missing libraries and install them"},
	})
	section("Catalog", [][]string{
		{"index refresh", "re-fetch and verify the signed catalog"},
		{"index show", "print the catalog source and cache paths"},
		{"index set <url>", "use another catalog source"},
	})
	section("Updates and help", [][]string{
		{"update [--check]", "check glp and installed apps for updates"},
		{"version", "print the version"},
		{"help", "print this menu"},
	})

	fmt.Println(outStyle("2", "Short names: s, i, ls, rm, up, fix"))
}

// outStyle only colours output that really goes to a terminal on stdout, so
// piping `glp help` into a file stays clean.
func outStyle(code, s string) string {
	if !animations() || !isTTY(os.Stdout) {
		return s
	}
	return style(code, s)
}

// errStyle is outStyle for messages written to stderr.
func errStyle(code, s string) string {
	if !animations() || !isTTY(os.Stderr) {
		return s
	}
	return style(code, s)
}
