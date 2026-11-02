package ec2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type fakeControl struct {
	started []string
	stopped []string
	err     error
}

func (f *fakeControl) StartInstances(ctx context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	f.started = append(f.started, in.InstanceIds...)
	return &ec2.StartInstancesOutput{}, f.err
}

func (f *fakeControl) StopInstances(ctx context.Context, in *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.stopped = append(f.stopped, in.InstanceIds...)
	return &ec2.StopInstancesOutput{}, f.err
}

func TestStartStop(t *testing.T) {
	f := &fakeControl{}
	if err := Start(context.Background(), f, "i-abc"); err != nil {
		t.Fatal(err)
	}
	if err := Stop(context.Background(), f, "i-def"); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 1 || f.started[0] != "i-abc" {
		t.Fatalf("unexpected started IDs: %v", f.started)
	}
	if len(f.stopped) != 1 || f.stopped[0] != "i-def" {
		t.Fatalf("unexpected stopped IDs: %v", f.stopped)
	}
}

func TestStartStopErrorsAreWrapped(t *testing.T) {
	f := &fakeControl{err: errors.New("denied")}
	if err := Start(context.Background(), f, "i-abc"); err == nil || !strings.Contains(err.Error(), "start instance i-abc") {
		t.Fatalf("unexpected start error: %v", err)
	}
	if err := Stop(context.Background(), f, "i-abc"); err == nil || !strings.Contains(err.Error(), "stop instance i-abc") {
		t.Fatalf("unexpected stop error: %v", err)
	}
}

func TestControlRejectsInvalidInstanceIDs(t *testing.T) {
	f := &fakeControl{}
	for _, id := range []string{"", "  ", "abc", "i-abc\n"} {
		if err := Start(context.Background(), f, id); err == nil {
			t.Errorf("Start(%q) unexpectedly succeeded", id)
		}
		if err := Stop(context.Background(), f, id); err == nil {
			t.Errorf("Stop(%q) unexpectedly succeeded", id)
		}
	}
	if len(f.started) != 0 || len(f.stopped) != 0 {
		t.Fatalf("invalid IDs reached the API: %v %v", f.started, f.stopped)
	}
}

func TestControlRejectsNilClient(t *testing.T) {
	if err := Start(context.Background(), nil, "i-abc"); err == nil {
		t.Fatal("expected nil client error")
	}
	if err := Stop(context.Background(), nil, "i-abc"); err == nil {
		t.Fatal("expected nil client error")
	}
}
