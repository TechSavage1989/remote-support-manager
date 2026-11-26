//go:build !windows

package main

import (
	"fmt"
	"os"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// On other systems the agent only runs as a simulator for local testing.
func newPlatform() (agentcore.Platform, func(), error) {
	if os.Getenv("RSM_SIMULATE") == "" && !simulate {
		return nil, nil, fmt.Errorf("the agent supports Windows only; use --simulate for a synthetic test endpoint")
	}
	dir, _ := os.Getwd()
	p := agentcore.NewSim(dir)
	code := os.Getenv("RSM_SIM_CODE")
	p.Consent = func(req protocol.SessionRequest, attempt int) agentcore.ConsentResult {
		fmt.Printf("Simulated endpoint: %s <%s> asks for %v\n", req.Technician, req.TechEmail, req.Perms)
		if code == "" {
			fmt.Print("Type the support code (empty to decline): ")
			_, _ = fmt.Scanln(&code)
		}
		c := code
		code = ""
		return agentcore.ConsentResult{Accept: c != "", Code: c, Perms: req.Perms}
	}
	return p, func() {}, nil
}

func messageBox(text string, warn bool) { fmt.Println(text) }
func confirm(text string) bool          { fmt.Println(text + " [assuming yes]"); return true }
