package awsconfig

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"go-aws/internal/model"
)

// Session carries the resolved AWS SDK configuration and account metadata.
type Session struct {
	Config    aws.Config
	AccountID string
	ARN       string
	Region    string
	Profile   string
}

// Resolve loads the standard AWS configuration chain. Explicit profile/region
// arguments take precedence. The returned session is validated with STS.
func Resolve(ctx context.Context, profileFlag, regionFlag string) (*Session, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithEC2IMDSRegion(),
	}
	if strings.TrimSpace(profileFlag) != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(strings.TrimSpace(profileFlag)))
	}
	if strings.TrimSpace(regionFlag) != "" {
		opts = append(opts, awsconfig.WithRegion(strings.TrimSpace(regionFlag)))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		return nil, fmt.Errorf("no AWS region resolved: pass --region, set AWS_REGION or AWS_DEFAULT_REGION, or configure a region in ~/.aws/config")
	}
	cfg.Region = region

	if cfg.Credentials == nil {
		return nil, fmt.Errorf("no AWS credentials resolved: run `aws sso login` or `aws configure`, set environment credentials, or pass --profile")
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return nil, fmt.Errorf("no usable AWS credentials: %w (run `aws sso login`/`aws configure`, set environment credentials, or pass --profile)", err)
	}

	client := sts.NewFromConfig(cfg)
	ident, err := client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("resolve AWS account with sts:GetCallerIdentity: %w", err)
	}
	if ident.Account == nil || *ident.Account == "" {
		return nil, fmt.Errorf("sts:GetCallerIdentity returned an empty account ID")
	}
	profile := strings.TrimSpace(profileFlag)
	if profile == "" {
		profile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	return &Session{
		Config:    cfg,
		AccountID: *ident.Account,
		ARN:       aws.ToString(ident.Arn),
		Region:    region,
		Profile:   profile,
	}, nil
}

// Credentials retrieves a fresh credential context for a new tunnel.
func (s *Session) Credentials(ctx context.Context) (model.Credentials, error) {
	if s == nil || s.Config.Credentials == nil {
		return model.Credentials{}, fmt.Errorf("AWS credentials unavailable")
	}
	creds, err := s.Config.Credentials.Retrieve(ctx)
	if err != nil {
		return model.Credentials{}, fmt.Errorf("retrieve AWS credentials: %w", err)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return model.Credentials{}, fmt.Errorf("AWS credential chain returned empty credentials")
	}
	return model.Credentials{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
	}, nil
}
