package client

import (
	"github.com/dearmachine/dearmachine/internal/hostos"
	"io"
	"os"
)

// Status readers must not deny deletion of the owner's ephemeral files on Windows.
func readDaemonFile(path string) ([]byte, error) {
	file, err := hostos.Open(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}
