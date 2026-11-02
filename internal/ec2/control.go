package ec2

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// ControlAPI is the EC2 surface used by Start and Stop.
type ControlAPI interface {
	StartInstances(ctx context.Context, params *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	StopInstances(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
}

// Start requests a start for a stopped instance.
func Start(ctx context.Context, client ControlAPI, instanceID string) error {
	id, err := controlInstanceID(instanceID)
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("nil EC2 client")
	}
	if _, err := client.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{id}}); err != nil {
		return fmt.Errorf("start instance %s: %w", id, err)
	}
	return nil
}

// Stop requests a normal (graceful) stop for a running instance.
func Stop(ctx context.Context, client ControlAPI, instanceID string) error {
	id, err := controlInstanceID(instanceID)
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("nil EC2 client")
	}
	if _, err := client.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{id}}); err != nil {
		return fmt.Errorf("stop instance %s: %w", id, err)
	}
	return nil
}

func controlInstanceID(instanceID string) (string, error) {
	if instanceID == "" {
		return "", fmt.Errorf("empty instance ID")
	}
	if !strings.HasPrefix(instanceID, "i-") || strings.ContainsAny(instanceID, " \t\r\n") {
		return "", fmt.Errorf("invalid instance ID %q", instanceID)
	}
	return instanceID, nil
}
