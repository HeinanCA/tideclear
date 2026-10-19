package cli

import (
	"context"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// realDeps builds the production adapters (the only place real clients are made).
func realDeps() Deps {
	return Deps{
		EKS: clientFactory(loadAWSConfig),
		DefaultRegion: func(profile string) (string, error) {
			cfg, err := loadAWSConfig(profile, "")
			if err != nil {
				return "", err
			}
			return cfg.Region, nil
		},
	}
}

// homeRegion is the config region for --all-regions runs; it only gives
// credential providers (STS) a region, each client overrides its own.
const homeRegion = "us-east-1"

func loadAWSConfig(profile, region string) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	return config.LoadDefaultConfig(context.Background(), opts...)
}

// clientFactory loads the AWS config once per profile, with the first
// requested region as its region, and derives each region's client from it by
// overriding Region. A load error is returned
// as-is: it is not specific to any region.
func clientFactory(load func(profile, region string) (aws.Config, error)) func(profile, region string) (eksinfo.EKSAPI, error) {
	type loaded struct {
		cfg aws.Config
		err error
	}
	cache := map[string]loaded{}
	return func(profile, region string) (eksinfo.EKSAPI, error) {
		l, ok := cache[profile]
		if !ok {
			l.cfg, l.err = load(profile, region)
			cache[profile] = l
		}
		if l.err != nil {
			return nil, l.err
		}
		return eks.NewFromConfig(l.cfg, func(o *eks.Options) { o.Region = region }), nil
	}
}
