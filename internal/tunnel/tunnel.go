package tunnel

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thezim/aicm/internal/model"
)

var portOpenedRE = regexp.MustCompile(`(?i)\bPort\s+([0-9]+)\s+opened\b`)

// AllocatePort asks the kernel for a free loopback TCP port.
func AllocatePort() (int, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// AWSBinary returns the AWS CLI executable. The environment override exists
// for integration tests.
func AWSBinary() string {
	if v := strings.TrimSpace(os.Getenv("AICM_AWS_BIN")); v != "" {
		return v
	}
	return "aws"
}

// Command builds the SSM port-forwarding command.
func Command(awsBin, instanceID string, remotePort, localPort int, region string) ([]string, error) {
	if awsBin == "" {
		awsBin = "aws"
	}
	if instanceID == "" {
		return nil, fmt.Errorf("empty instance ID")
	}
	if remotePort <= 0 || remotePort > 65535 || localPort <= 0 || localPort > 65535 {
		return nil, fmt.Errorf("invalid ports remote=%d local=%d", remotePort, localPort)
	}
	params, err := json.Marshal(map[string][]string{
		"portNumber":      {strconv.Itoa(remotePort)},
		"localPortNumber": {strconv.Itoa(localPort)},
	})
	if err != nil {
		return nil, err
	}
	args := []string{
		"ssm", "start-session",
		"--target", instanceID,
		"--document-name", "AWS-StartPortForwardingSession",
		"--parameters", string(params),
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	return append([]string{awsBin}, args...), nil
}

// ShellCommand builds an interactive SSM session command for an instance. It
// is run in the foreground by the TUI, not by the daemon.
func ShellCommand(awsBin, instanceID, region string) ([]string, error) {
	if awsBin == "" {
		awsBin = "aws"
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, fmt.Errorf("empty instance ID")
	}
	if strings.ContainsAny(instanceID, "\x00\r\n") {
		return nil, fmt.Errorf("invalid instance ID")
	}
	args := []string{
		"ssm", "start-session",
		"--target", instanceID,
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	return append([]string{awsBin}, args...), nil
}

// BuildEnv constructs a clean AWS CLI environment with per-connection
// credentials. The daemon's own AWS environment must not leak into the child.
func BuildEnv(base []string, creds model.Credentials, region string) []string {
	drop := map[string]bool{
		"AWS_ACCESS_KEY_ID":                      true,
		"AWS_ACCESS_KEY":                         true,
		"AWS_SECRET_ACCESS_KEY":                  true,
		"AWS_SECRET_KEY":                         true,
		"AWS_SESSION_TOKEN":                      true,
		"AWS_SECURITY_TOKEN":                     true,
		"AWS_CREDENTIAL_EXPIRATION":              true,
		"AWS_PROFILE":                            true,
		"AWS_DEFAULT_PROFILE":                    true,
		"AWS_REGION":                             true,
		"AWS_DEFAULT_REGION":                     true,
		"AWS_ROLE_ARN":                           true,
		"AWS_ROLE_SESSION_NAME":                  true,
		"AWS_WEB_IDENTITY_TOKEN_FILE":            true,
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": true,
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     true,
		"AWS_CONTAINER_AUTHORIZATION_TOKEN":      true,
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": true,
		"AWS_EC2_METADATA_DISABLED":              true,
	}
	out := make([]string, 0, len(base)+6)
	for _, kv := range base {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if drop[key] {
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"AWS_ACCESS_KEY_ID="+creds.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+creds.SecretAccessKey,
		"AWS_REGION="+region,
		"AWS_DEFAULT_REGION="+region,
		"AWS_EC2_METADATA_DISABLED=true",
		"AWS_PAGER=",
	)
	if creds.SessionToken != "" {
		out = append(out, "AWS_SESSION_TOKEN="+creds.SessionToken)
	}
	return out
}

// Ready reports whether SSM output says the local listener is open.
func Ready(output string) bool {
	return portOpenedRE.MatchString(output)
}

// PortOpen probes a loopback TCP port.
func PortOpen(port int) bool {
	c, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Tail is a bounded, concurrency-safe output capture.
type Tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func NewTail(max int) *Tail {
	if max <= 0 {
		max = 64 * 1024
	}
	return &Tail{max: max}
}

func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *Tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
