package gaetest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const output = `
INFO     2016-10-02 21:48:16,694 devappserver2.py:769] Skipping SDK update check.
INFO     2016-10-02 21:48:16,776 api_server.py:205] Starting API server at: http://localhost:36415
INFO     2016-10-02 21:48:16,904 dispatcher.py:197] Starting module "default" running at: http://localhost:8080
INFO     2016-10-02 21:48:16,905 admin_server.py:116] Starting admin server at: http://localhost:8000
`

func TestGetURLsOK(t *testing.T) {
	api, module, admin, err := getURLs(bytes.NewBufferString(output), time.Second)
	if err != nil {
		t.Fatalf("got error %q", err)
	}
	if expect := "http://localhost:36415"; api != expect {
		t.Fatalf("got %q, but expect %q", api, expect)
	}
	if expect := "http://localhost:8080"; module != expect {
		t.Fatalf("got %q, but expect %q", module, expect)
	}
	if expect := "http://localhost:8000"; admin != expect {
		t.Fatalf("got %q, but expect %q", admin, expect)
	}
}

func TestTimeout(t *testing.T) {
	pr, _ := io.Pipe()
	_, _, _, err := getURLs(pr, time.Second)
	expect := fmt.Errorf("timeout starting child process")
	if err.Error() != expect.Error() {
		t.Fatalf("got %#v, but expect %#v", err, expect)
	}
}

func TestScannerErr(t *testing.T) {
	pr, _ := io.Pipe()
	pr.CloseWithError(errors.New("scanner error"))
	_, _, _, err := getURLs(pr, 500*time.Millisecond)
	expect := errors.New("error reading server stderr: io: read/write on closed pipe")
	if err.Error() != expect.Error() {
		t.Fatalf("got %#v, but expect %#v", err, expect)
	}
}

const appYAML = `
application: gaetest
version: 1
runtime: go
api_version: go1
vm: true
handlers:
- url: /.*
  script: _go_app
`

const appSource = `
package main
import "google.golang.org/appengine"
func main() { appengine.Main()  }
`

func TestDevAppServer(t *testing.T) {
	appDir, err := ioutil.TempDir("", "gaetest")
	if err != nil {
		t.Fatalf("Got %v, expected nil", err)
	}
	defer os.RemoveAll(appDir)

	/*
		if err := os.mkdir(appDir, 0755); err != nil {
			t.Fatalf("Got got %s, expected nil", err)
		}
	*/
	err = ioutil.WriteFile(filepath.Join(appDir, "app.yaml"), []byte(appYAML), 0644)
	if err != nil {
		t.Fatalf("Got %v, expected nil", err)
	}
	err = ioutil.WriteFile(filepath.Join(appDir, "stubapp.go"), []byte(appSource), 0644)
	if err != nil {
		t.Fatalf("Got %v, expected nil", err)
	}

	sv, err := New(appDir, &Options{
		Port: 8080, AdminPort: 8000, Debug: testing.Verbose(), Timeout: 60,
	})
	if err != nil {
		t.Fatalf("New returned %v, expected nil", err)
	}

	var closed bool
	defer func() {
		if !closed {
			sv.Close()
		}
	}()
	if expect := "http://localhost:8080"; sv.ModuleURL != expect {
		t.Fatalf("Got Module URL %q, but expect %q", sv.ModuleURL, expect)
	}
	if expect := "http://localhost:8000"; sv.AdminURL != expect {
		t.Fatalf("Got Admin URL %q, but expect %q", sv.AdminURL, expect)
	}
	closed = true
	if err := sv.Close(); err != nil {
		t.Fatalf("Server.Close returned %v, expected nil", err)
	}
}
