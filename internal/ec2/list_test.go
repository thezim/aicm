package ec2

import (
	"context"
	"testing"
	"time"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type fakeAPI struct {
	t     *testing.T
	calls int
}

type filterCaptureAPI struct {
	filters []ec2types.Filter
}

func (f *filterCaptureAPI) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.filters = append([]ec2types.Filter(nil), in.Filters...)
	return &ec2.DescribeInstancesOutput{}, nil
}

func (f *fakeAPI) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.calls++
	if f.calls == 1 {
		if len(in.Filters) != 1 || in.Filters[0].Name == nil || *in.Filters[0].Name != "instance-state-name" {
			f.t.Errorf("unexpected filters: %+v", in.Filters)
		}
		return &ec2.DescribeInstancesOutput{
			NextToken: aws.String("page2"),
			Reservations: []ec2types.Reservation{{
				Instances: []ec2types.Instance{{
					InstanceId:       aws.String("i-1"),
					State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
					InstanceType:     ec2types.InstanceTypeT3Micro,
					PrivateIpAddress: aws.String("10.0.0.1"),
					LaunchTime:       aws.Time(time.Unix(100, 0)),
					Tags: []ec2types.Tag{
						{Key: aws.String("Name"), Value: aws.String("web")},
						{Key: aws.String("Environment"), Value: aws.String("prod")},
					},
					PlatformDetails: aws.String("Linux/UNIX"),
				}},
			}},
		}, nil
	}
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{
			Instances: []ec2types.Instance{{
				InstanceId:   aws.String("i-2"),
				State:        &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped},
				InstanceType: ec2types.InstanceTypeM5Large,
				LaunchTime:   aws.Time(time.Unix(200, 0)),
				Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("db")}},
			}},
		}},
	}, nil
}

func TestListPaginatesAndConverts(t *testing.T) {
	f := &fakeAPI{t: t}
	got, err := List(context.Background(), f, []string{"running", "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Fatalf("expected 2 pages, got %d", f.calls)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances", len(got))
	}
	if got[0].Name != "web" || got[0].State != "running" || got[0].PrivateIP != "10.0.0.1" || got[0].Platform != "Linux/UNIX" {
		t.Fatalf("unexpected first instance: %+v", got[0])
	}
	if len(got[0].Tags) != 2 || got[0].Tags[0].Key != "Name" || got[0].Tags[1].Value != "prod" {
		t.Fatalf("unexpected tags: %+v", got[0].Tags)
	}
	if got[1].Name != "db" || got[1].State != "stopped" {
		t.Fatalf("unexpected second instance: %+v", got[1])
	}
}

func TestListEmptyStatesOmitsFilter(t *testing.T) {
	f := &filterCaptureAPI{}
	if _, err := List(context.Background(), f, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.filters) != 0 {
		t.Fatalf("expected no state filter, got %+v", f.filters)
	}
}

func TestInstanceIsWindows(t *testing.T) {
	cases := []struct {
		platform string
		want     bool
	}{
		{"Windows", true},
		{"windows", true},
		{"Windows (SQL Server)", true},
		{"Linux/UNIX", false},
		{"Red Hat Enterprise Linux", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := (Instance{Platform: tc.platform}).IsWindows(); got != tc.want {
			t.Errorf("IsWindows(%q)=%v want %v", tc.platform, got, tc.want)
		}
	}
}
