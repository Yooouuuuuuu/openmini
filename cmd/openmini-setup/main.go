// openmini-setup checks what openmini needs on this machine — the agy CLI
// and its sign-in, the browser and the Gemini sign-in — and asks only about
// what is missing. Run it the first time, and again whenever a sign-in has
// lapsed. It sits next to openmini.exe and works on that folder's config.toml.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"openmini/internal/setup"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	dir := filepath.Dir(exe)
	if err := os.Chdir(dir); err != nil { // config.toml and data/ live next to the exes
		fmt.Println(err)
		os.Exit(1)
	}
	server := "openmini"
	if runtime.GOOS == "windows" {
		server += ".exe"
	}
	fmt.Println("openmini setup: checks agy and the Gemini web app, and asks only about what is missing.")
	code := 0
	if err := setup.Run("config.toml", filepath.Join(dir, server)); err != nil {
		fmt.Println("setup stopped:", err)
		code = 1
	}
	if runtime.GOOS == "windows" { // double-clicked: keep the window open to read the result
		fmt.Print("\nPress Enter to close this window. ")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	os.Exit(code)
}
