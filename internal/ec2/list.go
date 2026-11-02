package ec2

import (
	"context"
	"fmt"
	"strings"
	"time"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Tag is an EC2 resource tag.
type Tag struct {
	Key   string
	Value string
}

// Instance is the subset of EC2 data used by the TUI.
type Instance struct {
	InstanceID       string
	Name             string
	State            string
	Type             string
	PrivateIP        string
	Platform         string
	LaunchTime       time.Time
	AvailabilityZone string
	Tags             []Tag
}

// IsWindows reports whether the instance runs a Windows platform. RDP
// forwarding is only supported for these instances.
func (i Instance) IsWindows() bool {
	return strings.Contains(strings.ToLower(i.Platform), "windows")
}

// NewClient constructs an EC2 client from a resolved AWS config.
func NewClient(cfg aws.Config) *ec2.Client {
	return ec2.NewFromConfig(cfg)
}

// API is the EC2 client surface used by List, enabling tests with fakes.
type API interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// List returns instances whose states match the filter. An empty filter
// returns instances in every state.
func List(ctx context.Context, client API, states []string) ([]Instance, error) {
	if client == nil {
		return nil, fmt.Errorf("nil EC2 client")
	}
	filterStates := make([]ec2types.InstanceStateName, 0, len(states))
	for _, s := range states {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "pending":
			filterStates = append(filterStates, ec2types.InstanceStateNamePending)
		case "running":
			filterStates = append(filterStates, ec2types.InstanceStateNameRunning)
		case "shutting-down":
			filterStates = append(filterStates, ec2types.InstanceStateNameShuttingDown)
		case "terminated":
			filterStates = append(filterStates, ec2types.InstanceStateNameTerminated)
		case "stopping":
			filterStates = append(filterStates, ec2types.InstanceStateNameStopping)
		case "stopped":
			filterStates = append(filterStates, ec2types.InstanceStateNameStopped)
		}
	}
	input := &ec2.DescribeInstancesInput{}
	if len(filterStates) > 0 {
		input.Filters = []ec2types.Filter{{
			Name:   aws.String("instance-state-name"),
			Values: stateStrings(filterStates),
		}}
	}
	var out []Instance
	paginator := ec2.NewDescribeInstancesPaginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe instances: %w", err)
		}
		for _, reservation := range page.Reservations {
			for _, inst := range reservation.Instances {
				out = append(out, convert(inst))
			}
		}
	}
	return out, nil
}

func stateStrings(in []ec2types.InstanceStateName) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func convert(inst ec2types.Instance) Instance {
	i := Instance{
		InstanceID: aws.ToString(inst.InstanceId),
		State:      string(inst.State.Name),
		Type:       string(inst.InstanceType),
		PrivateIP:  aws.ToString(inst.PrivateIpAddress),
		LaunchTime: aws.ToTime(inst.LaunchTime),
	}
	if inst.Placement != nil {
		i.AvailabilityZone = aws.ToString(inst.Placement.AvailabilityZone)
	}
	if inst.PlatformDetails != nil && *inst.PlatformDetails != "" {
		i.Platform = *inst.PlatformDetails
	} else if inst.Platform != "" {
		i.Platform = string(inst.Platform)
	}
	for _, tag := range inst.Tags {
		key := aws.ToString(tag.Key)
		value := aws.ToString(tag.Value)
		i.Tags = append(i.Tags, Tag{Key: key, Value: value})
		if key == "Name" {
			i.Name = value
		}
	}
	return i
}
