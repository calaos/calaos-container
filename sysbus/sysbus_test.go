package sysbus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	logger "github.com/calaos/calaos-container/log"
)

func TestJobWait(t *testing.T) {
	for _, result := range []string{JobDone, "failed", "timeout", "canceled", "dependency", "skipped", "", "whatever"} {
		t.Run(result, func(t *testing.T) {
			ch := make(chan string, 1)
			ch <- result
			released := 0
			err := NewJob("x.service", ch, func() { released++ }).Wait(context.Background())
			if released != 1 {
				t.Fatalf("released %d times", released)
			}
			if result == JobDone {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var je *JobError
			if !errors.As(err, &je) || je.Result != result || je.Unit != "x.service" {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestJobWaitGivesUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	released := false
	err := NewJob("x.service", make(chan string), func() { released = true }).Wait(ctx)
	var je *JobError
	if !errors.As(err, &je) || je.Result != JobUnfinished || !released {
		t.Fatalf("%v, released %v", err, released)
	}
}

func TestParseUnitProperties(t *testing.T) {
	p, err := parseUnitProperties(map[string]interface{}{
		"LoadState": "loaded", "ActiveState": "active", "SubState": "running",
		"UnitFileState": "enabled", "ActiveEnterTimestamp": uint64(1793000472000000),
	})
	want := UnitProperties{"loaded", "active", "running", "enabled", 1793000472000000}
	if err != nil || p != want {
		t.Fatalf("%+v, %v", p, err)
	}

	// An unset property reads as its zero value, and so does a wrong type.
	p, err = parseUnitProperties(map[string]interface{}{"LoadState": "not-found", "ActiveEnterTimestamp": "soon"})
	if err != nil || p != (UnitProperties{LoadState: "not-found"}) {
		t.Fatalf("%+v, %v", p, err)
	}

	for _, raw := range []map[string]interface{}{nil, {"ActiveState": "active"}, {"LoadState": ""}, {"LoadState": 3}} {
		if p, err := parseUnitProperties(raw); err == nil {
			t.Fatalf("%v parsed as %+v", raw, p)
		}
	}
}

func TestFailHidesBusText(t *testing.T) {
	var logs bytes.Buffer
	logger.Logger.SetOutput(&logs)
	t.Cleanup(func() { logger.Logger.SetOutput(os.Stdout) })

	const detail = "org.freedesktop.DBus.Error.AccessDenied: /etc/shadow"
	err := fail("set time", errors.New(detail))
	if err.Error() != "set time failed" {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(logs.String(), detail) {
		t.Fatalf("not logged: %s", logs.String())
	}
}
