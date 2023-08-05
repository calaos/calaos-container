package models

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
)

func RunCommand(command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)

	logging.Debugf("RunCommand: %s %v", command, args)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()

	if err != nil {
		e := fmt.Errorf("RunCommand error: %s, stderr: %s", err, stderr.String())
		logging.Error(e)
		return "", e
	}

	return stdout.String(), nil
}

func RunCommandReader(command string, args ...string) (io.Reader, error) {
	cmd := exec.Command(command, args...)

	// Create a pipe to capture the command's stdout.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return stdoutPipe, nil
}
