package main

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/gookit/gcli/v3"

	"openmini/internal/config"
	"openmini/internal/setup"
)

// selfExe is this executable's path; the shortcuts point at it.
func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "openmini"
	}
	return exe
}

// setupExe is openmini-setup next to this executable, or "" when it is not there.
func setupExe() string {
	name := "openmini-setup"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(selfExe()), name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// loginCmd signs in to the Gemini web app once.
func loginCmd() *gcli.Command {
	return &gcli.Command{
		Name: "login", Desc: "open a browser window to sign in to the Gemini web app once",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			return setup.WebLogin(cfg)
		},
	}
}

// setupCmd runs the same checks as openmini-setup.exe.
func setupCmd() *gcli.Command {
	return &gcli.Command{
		Name: "setup", Desc: "check agy and the browser, sign in where needed, write config.toml (same as openmini-setup)",
		Config: withConfigOpt,
		Func: func(c *gcli.Command, _ []string) error {
			return setup.Run(cfgPath, selfExe())
		},
	}
}
