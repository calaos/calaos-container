package models

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
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

func RunCommandReader(command, logfile, exitfile string, args ...string) (io.Reader, error) {
	cmd := exec.Command(command, args...)

	allWriters := make([]io.Writer, 0, 2)

	var log *os.File
	var err error

	if logfile != "" {
		log, err = os.Create(logfile)
		if err != nil {
			return nil, err
		}

		allWriters = append(allWriters, log)
	}

	pipeReader, pipeWriter := io.Pipe()
	allWriters = append(allWriters, pipeWriter)

	mw := io.MultiWriter(allWriters...)
	cmd.Stdout = mw
	//Also writes stderr to stdout
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go func() {
		err := cmd.Wait()
		pipeWriter.Close()
		log.Close()

		if werr, ok := err.(*exec.ExitError); ok && exitfile != "" {
			exitCode := werr.ExitCode()
			fmt.Printf("Command exited with status: %d\n", exitCode)
			err := os.WriteFile(exitfile, []byte(strconv.Itoa(exitCode)), 0644)
			if err != nil {
				fmt.Printf("Error writing to file %s: %v", exitfile, err)
				return
			}
		} else {
			err := os.WriteFile(exitfile, []byte("0"), 0644)
			if err != nil {
				fmt.Printf("Error writing to file %s: %v", exitfile, err)
				return
			}
		}
	}()

	return pipeReader, nil
}
