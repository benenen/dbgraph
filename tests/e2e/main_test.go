package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestMain(run *testing.M) {
	if _, err := os.Stat("../../web/node_modules"); os.IsNotExist(err) {
		install := exec.Command("npm", "ci")
		install.Dir = "../../web"
		install.Stdout = os.Stdout
		install.Stderr = os.Stderr
		if err := install.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "install Vue console dependencies for E2E: %v\n", err)
			os.Exit(1)
		}
	}
	build := exec.Command("npm", "run", "build")
	build.Dir = "../../web"
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build Vue console for E2E: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run.Run())
}
