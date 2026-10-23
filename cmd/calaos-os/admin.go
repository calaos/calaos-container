package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	cli "github.com/jawher/mow.cli"
	"golang.org/x/term"

	"github.com/calaos/calaos-container/cmd/calaos-os/api"
)

const (
	adminMinLength   = 10
	adminMaxBytes    = 1024
	adminInputBuffer = 4096
)

func cmdAdmin(cmd *cli.Cmd) {
	cmd.Command("set", "set the admin password, read from stdin (asked twice on a terminal, first line otherwise)", cmdAdminSet)
	cmd.Command("reset", "remove the admin password and close every admin session", cmdAdminReset)
}

// The password never comes from argv: RunCommand logs argv and ps shows it.
func cmdAdminSet(cmd *cli.Cmd) {
	cmd.Spec = ""
	cmd.Action = func() {
		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		fd := int(os.Stdin.Fd()) //nolint:gosec // a file descriptor fits in an int
		pw, err := readNewPassword(os.Stdin, os.Stderr, term.IsTerminal(fd), func() ([]byte, error) {
			return term.ReadPassword(fd)
		})
		if err != nil {
			exit(err, ExitUsage)
		}

		a := api.NewCalaosApi(api.CalaosCtHost)
		if _, err := a.AdminSet(token, pw); err != nil {
			exitAdminErr(err)
		}
		fmt.Printf("%s Admin password set, every admin session closed.\n", green(CharCheck))
	}
}

func cmdAdminReset(cmd *cli.Cmd) {
	cmd.Spec = ""
	cmd.Action = func() {
		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		a := api.NewCalaosApi(api.CalaosCtHost)
		wasSet, err := a.AdminReset(token)
		if err != nil {
			exitAdminErr(err)
		}
		if !wasSet {
			fmt.Printf("%s No admin password was set.\n", green(CharCheck))
			return
		}
		fmt.Printf("%s Admin password removed, every admin session closed.\n", green(CharCheck))
		// Anyone who knows the household password can now bootstrap the admin.
		fmt.Println(cyan(CharArrow), "Change the household password before setting a new admin password from the web app.")
	}
}

func exitAdminErr(err error) {
	var ae *api.APIError
	switch {
	case errors.Is(err, api.ErrNoAdmin):
		exit(err, ExitServer)
	case errors.As(err, &ae) && ae.Code == "password_too_short":
		exit(fmt.Errorf("the admin password needs at least %d characters", adminMinLength), ExitUsage)
	case errors.As(err, &ae) && ae.Code == "invalid_request":
		exit(err, ExitUsage)
	case errors.As(err, &ae) && ae.Code == "rate_limited":
		exit(errors.New("another admin password operation is running, retry in a moment"), ExitServer)
	}
	exitAPIErr(err)
}

// readNewPassword asks twice without echo on a terminal, where a typo is
// possible; a script pipes the password as the first line of stdin.
func readNewPassword(in io.Reader, prompt io.Writer, tty bool, readSecret func() ([]byte, error)) (string, error) {
	if !tty {
		return readPasswordLine(in)
	}

	ask := func(label string) ([]byte, error) {
		_, _ = fmt.Fprint(prompt, label)
		b, err := readSecret()
		_, _ = fmt.Fprintln(prompt)
		return b, err
	}
	first, err := ask("New admin password: ")
	if err != nil {
		return "", err
	}
	second, err := ask("Repeat the admin password: ")
	if err != nil {
		return "", err
	}
	if !bytes.Equal(first, second) {
		return "", errors.New("the passwords do not match")
	}
	return checkPasswordInput(first)
}

func readPasswordLine(in io.Reader) (string, error) {
	line, err := bufio.NewReaderSize(in, adminInputBuffer).ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return "", fmt.Errorf("the admin password is longer than %d bytes", adminMaxBytes)
	case err != nil && !errors.Is(err, io.EOF):
		return "", err
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return checkPasswordInput(line)
}

func checkPasswordInput(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errors.New("no admin password on stdin")
	}
	if len(b) > adminMaxBytes {
		return "", fmt.Errorf("the admin password is longer than %d bytes", adminMaxBytes)
	}
	return string(b), nil
}
