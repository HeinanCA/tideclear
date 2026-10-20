package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// realDeps builds the production adapters (the only place real clients are made).
func realDeps() Deps {
	cfg := cachedConfig(loadAWSConfig)
	return Deps{
		EKS:          clientFor(cfg, buildEKS),
		Cluster:      clientFor(cfg, buildCluster),
		Kube:         realKube,
		KubeIdentity: kubeIdentity,
		DefaultRegion: func(profile string) (string, error) {
			cfg, err := loadAWSConfig(profile, "")
			if err != nil {
				return "", err
			}
			return cfg.Region, nil
		},
	}
}

// kubeTimeout bounds each API request so an unresponsive server cannot hang a scan.
const kubeTimeout = 30 * time.Second

var silenceKlog sync.Once

// realKube builds discovery and dynamic clients from the standard kubeconfig
// rules ($KUBECONFIG honored). Its errors are single-line.
func realKube(kubeconfig, kubeContext string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
	cfg, err := kubeRestConfig(kubeconfig, kubeContext)
	if err != nil {
		return nil, nil, err
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("kubernetes client: %s", oneLine(err.Error()))
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("kubernetes client: %s", oneLine(err.Error()))
	}
	return disc, dyn, nil
}

// kubeIdentity names the context and API server host a scan would use, for
// the report header. Unknown parts are empty.
func kubeIdentity(kubeconfig, kubeContext string) (name, server string) {
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	loading.ExplicitPath = kubeconfig
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, &clientcmd.ConfigOverrides{CurrentContext: kubeContext})
	if raw, err := cc.RawConfig(); err == nil {
		name = raw.CurrentContext
		if kubeContext != "" {
			name = kubeContext
		}
	}
	if cfg, err := cc.ClientConfig(); err == nil {
		server = cfg.Host
		if u, err := url.Parse(cfg.Host); err == nil && u.Host != "" {
			server = u.Host
		}
	}
	return name, server
}

func kubeRestConfig(kubeconfig, kubeContext string) (*rest.Config, error) {
	// client-go logs through klog to stderr; failures must stay one line.
	silenceKlog.Do(func() {
		// LogToStderr(false) alone still sends ERROR to stderr via the
		// stderr threshold, so raise it too.
		fs := flag.NewFlagSet("klog", flag.ContinueOnError)
		klog.InitFlags(fs)
		_ = fs.Set("logtostderr", "false")
		_ = fs.Set("stderrthreshold", "FATAL")
		klog.SetOutput(io.Discard)
	})
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	loading.ExplicitPath = kubeconfig
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loading, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	switch {
	case clientcmd.IsEmptyConfig(err):
		return nil, errors.New("no kubeconfig found: set --kubeconfig or KUBECONFIG")
	case err != nil:
		return nil, fmt.Errorf("load kubeconfig: %s", oneLine(err.Error()))
	}
	cfg.WarningHandler = rest.NoWarnings{}
	cfg.Timeout = kubeTimeout
	return cfg, nil
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

// cachedConfig loads the AWS config once per profile, with the first
// requested region as its region. A load error is cached and returned as-is:
// it is not specific to any region.
func cachedConfig(load func(profile, region string) (aws.Config, error)) func(profile, region string) (aws.Config, error) {
	type loaded struct {
		cfg aws.Config
		err error
	}
	cache := map[string]loaded{}
	return func(profile, region string) (aws.Config, error) {
		l, ok := cache[profile]
		if !ok {
			l.cfg, l.err = load(profile, region)
			cache[profile] = l
		}
		return l.cfg, l.err
	}
}

// clientFor derives a client per region from the cached config by overriding Region.
func clientFor[T any](cfg func(profile, region string) (aws.Config, error), build func(aws.Config, string) T) func(profile, region string) (T, error) {
	return func(profile, region string) (T, error) {
		c, err := cfg(profile, region)
		if err != nil {
			var zero T
			return zero, err
		}
		return build(c, region), nil
	}
}

func buildEKS(cfg aws.Config, region string) eksinfo.EKSAPI {
	return eks.NewFromConfig(cfg, func(o *eks.Options) { o.Region = region })
}

func buildCluster(cfg aws.Config, region string) ClusterAPI {
	return eks.NewFromConfig(cfg, func(o *eks.Options) { o.Region = region })
}

// clientFactory builds EKS clients from a config cached per profile.
func clientFactory(load func(profile, region string) (aws.Config, error)) func(profile, region string) (eksinfo.EKSAPI, error) {
	return clientFor(cachedConfig(load), buildEKS)
}
