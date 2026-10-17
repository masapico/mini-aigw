package services

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"mini-aigw/internal/config"
)

type AWSClientBundle struct {
	BedrockRuntime      *bedrockruntime.Client
	BedrockAgentRuntime *bedrockagentruntime.Client
	BedrockAgent        *bedrockagent.Client
	S3                  *s3.Client
	Config              *config.Config
}

func NewAWSClientBundle(ctx context.Context, cfg *config.Config) (*AWSClientBundle, error) {
	httpClient := &http.Client{
		Timeout: time.Duration(cfg.AWSReadTimeoutSec) * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.AWSMaxPoolConnections,
			MaxIdleConnsPerHost: cfg.AWSMaxPoolConnections,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	loadOpts := []func(*awsConfig.LoadOptions) error{
		awsConfig.WithRegion(cfg.AWSRegion),
		awsConfig.WithHTTPClient(httpClient),
		awsConfig.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = cfg.AWSMaxRetryAttempts
			})
		}),
	}

	awsCfg, err := awsConfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, err
	}

	bedrockRuntimeOpts := []func(*bedrockruntime.Options){}
	bedrockAgentRuntimeOpts := []func(*bedrockagentruntime.Options){}
	bedrockAgentOpts := []func(*bedrockagent.Options){}
	s3Opts := []func(*s3.Options){}

	if cfg.AWSEndpointURL != "" {
		endpointURL := cfg.AWSEndpointURL
		bedrockRuntimeOpts = append(bedrockRuntimeOpts, func(o *bedrockruntime.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
		})
		bedrockAgentRuntimeOpts = append(bedrockAgentRuntimeOpts, func(o *bedrockagentruntime.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
		})
		bedrockAgentOpts = append(bedrockAgentOpts, func(o *bedrockagent.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
		})
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
			o.UsePathStyle = true
		})
	}

	bundle := &AWSClientBundle{
		BedrockRuntime:      bedrockruntime.NewFromConfig(awsCfg, bedrockRuntimeOpts...),
		BedrockAgentRuntime: bedrockagentruntime.NewFromConfig(awsCfg, bedrockAgentRuntimeOpts...),
		BedrockAgent:        bedrockagent.NewFromConfig(awsCfg, bedrockAgentOpts...),
		S3:                  s3.NewFromConfig(awsCfg, s3Opts...),
		Config:              cfg,
	}

	return bundle, nil
}

