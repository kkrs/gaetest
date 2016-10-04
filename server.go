/*
Package gaetest is a modified version of https://godoc.org/google.golang.org/appengine/aetest.
It provides an API for running Go App Engine apps under the development server.
The main use case is to start the app in a harness so tests can be run against
it.
*/
package gaetest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

// TODO(kkrs): Add the capability to run dev_appserver on particular ports.
type Options struct {
	// Path to the dev app server. An atttempt to search for it on $PATH will be
	// made. Defaults to "dev_appserver.py".
	DevAppServer string
	// Host to which the application and admin modules should bind to. The value
	// is passed to the arguments --host and --admin_host. Defaults to "localhost".
	Host string
	// Port to which the application module binds to. Defaults to a random high
	// port. This behaviour is different from dev_appserver.py which binds to
	// 8080 by default.
	Port int
	// Port to which the admin module binds to. Defaults to a random high port.
	// This behaviour is different from dev_appserver.py which binds to 8000 by
	// default.
	AdminPort int
	// Timeout in seconds used to wait for appserver startup and close. Defaults
	// to 15s.
	Timeout int
	// Print debug output.
	Debug bool
}

type Server struct {
	appDir    string
	opts      *Options
	child     *exec.Cmd
	AdminURL  string
	APIURL    string
	ModuleURL string
}

// New launches an instance dev_appserver to run the app at appDir. If opts is
// nil the default values are used. If New returns without errors,
// Server.ModuleURL contains the endpoint to run the tests against.
func New(appDir string, opts *Options) (*Server, error) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.DevAppServer == "" {
		opts.DevAppServer = "dev_appserver.py"
	}
	if opts.Host == "" {
		opts.Host = "localhost"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 15
	}
	sv := &Server{appDir: appDir, opts: opts}
	return sv, sv.run()
}

var apiServerAddrRE = regexp.MustCompile(`Starting API server at: (\S+)`)
var moduleServerAddrRE = regexp.MustCompile(`Starting module ".+" running at: (\S+)`)
var adminServerAddrRE = regexp.MustCompile(`Starting admin server at: (\S+)`)

func getURLs(reader io.Reader, timeout time.Duration) (string, string, string, error) {
	var (
		api, module, admin string
		errc               = make(chan error, 1)
	)

	scanned := func() bool {
		return (api != "" && module != "" && admin != "")
	}

	go func() { // scan stderr for patterns
		s := bufio.NewScanner(reader)
		// The test scanned must be performed before Scan is called, or else the scanner could block
		// waiting for the next line. This reads much better than an if block at the end of the for
		// loop.
		for !scanned() && s.Scan() {
			if match := apiServerAddrRE.FindStringSubmatch(s.Text()); match != nil {
				api = match[1]
			}
			if match := moduleServerAddrRE.FindStringSubmatch(s.Text()); match != nil {
				module = match[1]
			}
			if match := adminServerAddrRE.FindStringSubmatch(s.Text()); match != nil {
				admin = match[1]
			}
		}
		errc <- s.Err()
	}()

	select {
	case <-time.After(timeout):
		return "", "", "", fmt.Errorf("timeout starting child process")
	case err := <-errc:
		if err != nil {
			return "", "", "", fmt.Errorf("error reading server stderr: %v", err)
		}
	}

	if admin == "" {
		return "", "", "", errors.New("unable to find admin server URL")
	}
	if module == "" {
		return "", "", "", errors.New("unable to find module server URL")
	}
	if api == "" {
		return "", "", "", errors.New("unable to find api server URL")
	}

	return api, module, admin, nil
}

func (sv *Server) run() error {
	serverPath, err := exec.LookPath(sv.opts.DevAppServer)
	if err != nil {
		return err
	}

	args := []string{
		"--automatic_restart=false",
		"--skip_sdk_update_check=true",
		"--clear_datastore=true",
		"--clear_search_indexes=true",
		"--datastore_consistency_policy=consistent",
		fmt.Sprintf("--host=%s", sv.opts.Host),
		fmt.Sprintf("--admin_host=%s", sv.opts.Host),
		fmt.Sprintf("--port=%d", sv.opts.Port),
		fmt.Sprintf("--admin_port=%d", sv.opts.AdminPort),
		sv.appDir,
	}

	if sv.opts.Debug {
		log.Printf("running %s %v\n\n", serverPath, args)
	}

	sv.child = exec.Command(serverPath, args...)

	// print stdout, stderr only if debug is set.
	stdout := ioutil.Discard
	if sv.opts.Debug {
		stdout = os.Stdout
	}
	sv.child.Stdout = stdout

	var stderr io.Reader
	stderr, err = sv.child.StderrPipe()
	if err != nil {
		return err
	}

	if sv.opts.Debug {
		stderr = io.TeeReader(stderr, os.Stderr)
	}

	sv.child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sv.child.Start(); err != nil {
		return err
	}

	sv.APIURL, sv.ModuleURL, sv.AdminURL, err = getURLs(stderr, time.Duration(sv.opts.Timeout)*time.Second)
	if err != nil {
		sv.kill()
	}
	return err
}

func (sv *Server) kill() {
	// kill all processes in the same gid
	if err := syscall.Kill(-sv.child.Process.Pid, syscall.SIGKILL); err != nil && sv.opts.Debug {
		log.Printf("syscall.Kill: got %v, expected nil", err)
	}
}

// Close kills the child dev_appserver process, releasing its resources.
func (sv *Server) Close() error {
	if sv.child.Process == nil {
		return nil
	}

	errc := make(chan error, 1)

	if sv.opts.Debug {
		log.Printf("attempting to stop %s", sv.child.Path)
	}

	go func() {
		errc <- sv.child.Wait()
	}()

	if sv.opts.Debug {
		log.Printf("calling /quit handler on the admin server")
	}
	res, err := http.Get(sv.AdminURL + "/quit")
	if err != nil {
		sv.kill()
		return fmt.Errorf("unable to call /quit handler: %v", err)
	}
	res.Body.Close()

	select {
	case <-time.After(time.Duration(sv.opts.Timeout) * time.Second):
		sv.kill()
		return errors.New("timeout killing child process")
	case err := <-errc:
		return err
	}
	return nil
}
