package tunnel

import (
	"encoding/json"
	"strings"
	"testing"

	"go-aws/internal/model"
)

func TestCommand(t *testing.T) {
	args, err := Command("aws", "i-abc", 3389, 50123, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 9 || args[0] != "aws" || args[1] != "ssm" || args[2] != "start-session" {
		t.Fatalf("unexpected args: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--target i-abc", "AWS-StartPortForwardingSession", "--region us-east-1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	var params map[string][]string
	if err := json.Unmarshal([]byte(args[8]), &params); err != nil {
		t.Fatalf("parameters not JSON: %v (%q)", err, args[8])
	}
	if params["portNumber"][0] != "3389" || params["localPortNumber"][0] != "50123" {
		t.Fatalf("unexpected parameters: %v", params)
	}
}

func TestShellCommand(t *testing.T) {
	args, err := ShellCommand("aws", "i-abc", "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 7 || args[0] != "aws" || args[1] != "ssm" || args[2] != "start-session" {
		t.Fatalf("unexpected args: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--target i-abc", "--region us-west-2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if _, err := ShellCommand("aws", "", "us-east-1"); err == nil {
		t.Fatal("expected error for empty instance ID")
	}
}

func TestBuildEnvStripsInheritedAWSAuth(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"AWS_PROFILE=prod",
		"AWS_ACCESS_KEY_ID=OLD",
		"AWS_SECRET_ACCESS_KEY=OLD",
		"AWS_SESSION_TOKEN=OLD",
		"AWS_REGION=eu-west-1",
		"AWS_DEFAULT_REGION=eu-west-1",
		"AWS_WEB_IDENTITY_TOKEN_FILE=/tmp/token",
		"HOME=/home/user",
	}
	env := BuildEnv(base, model.Credentials{
		AccessKeyID:     "NEWKEY",
		SecretAccessKey: "NEWSECRET",
		SessionToken:    "NEWTOKEN",
	}, "us-west-2")
	joined := strings.Join(env, "\n")
	for _, banned := range []string{"OLD", "AWS_PROFILE=", "AWS_REGION=eu-west-1", "AWS_WEB_IDENTITY_TOKEN_FILE="} {
		if strings.Contains(joined, banned) {
			t.Fatalf("%q leaked into child env:\n%s", banned, joined)
		}
	}
	for _, want := range []string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"AWS_ACCESS_KEY_ID=NEWKEY",
		"AWS_SECRET_ACCESS_KEY=NEWSECRET",
		"AWS_SESSION_TOKEN=NEWTOKEN",
		"AWS_REGION=us-west-2",
		"AWS_DEFAULT_REGION=us-west-2",
		"AWS_EC2_METADATA_DISABLED=true",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestReady(t *testing.T) {
	cases := map[string]bool{
		"Port 50123 opened for sessionId abc":                       true,
		"Starting session...\nPort 1234 opened for sessionId xyz\n": true,
		"port 8080 OPENED for sessionId":                            true,
		"Waiting for connections...":                                false,
		"Port opened":                                               false,
	}
	for in, want := range cases {
		if got := Ready(in); got != want {
			t.Errorf("Ready(%q)=%v want %v", in, got, want)
		}
	}
}

func TestTailBounded(t *testing.T) {
	tail := NewTail(10)
	_, _ = tail.Write([]byte("0123456789abcdef"))
	if len(tail.String()) != 10 {
		t.Fatalf("tail not bounded: %q", tail.String())
	}
	if tail.String() != "6789abcdef" {
		t.Fatalf("unexpected tail: %q", tail.String())
	}
}
