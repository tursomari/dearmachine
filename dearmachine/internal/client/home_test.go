package client

import (
	"os"
	"testing"
)

// Sync now reads the user model selection. Never let a unit test read the
// developer's installed model configuration (including parallel fixtures).
func TestMain(m *testing.M) {
	if os.Getenv("DEARMACHINE_TEST_HOME_ISOLATED") == "1" {
		os.Exit(m.Run())
	}
	if err := os.Setenv("DEARMACHINE_TEST_HOME_ISOLATED", "1"); err != nil {
		panic(err)
	}
	home, err := os.MkdirTemp("", "dearmachine-test-home-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	if err := os.Setenv("USERPROFILE", home); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
