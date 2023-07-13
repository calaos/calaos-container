package main

import (
	"context"
	"fmt"
	"os"

	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/images"
	//"github.com/coreos/go-systemd/v22/dbus"
	//godbus "github.com/godbus/dbus/v5"
)

/*
 Podman API Docs:
 https://pkg.go.dev/github.com/containers/podman/v4@v4.5.1/pkg/bindings#section-readme
*/

func main() {
	conn, err := bindings.NewConnection(context.Background(), "unix://run/podman/podman.sock")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	_, err = images.Pull(conn, "ghcr.io/calaos/calaos_home", nil)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
