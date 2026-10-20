package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
)

type offlineHTTP struct{}

func (offlineHTTP) Do(*http.Request) (*http.Response, error) { return nil, errors.New("network down") }

// loadAWSConfig must wrap the provider: the SDK does not tag a failing
// credential_process itself, so without the wrap the run would not stop.
func TestLoadAWSConfigTagsCredentialProviderFailure(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgFile, []byte("[profile broken]\ncredential_process = sh -c 'exit 1'\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgFile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "missing"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	cfg, err := loadAWSConfig("broken", "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	c := eks.NewFromConfig(cfg, func(o *eks.Options) { o.HTTPClient = offlineHTTP{}; o.RetryMaxAttempts = 1 })
	_, err = c.ListInsights(context.Background(), &eks.ListInsightsInput{ClusterName: aws.String("c")})
	if err == nil || !eksinfo.IsCredentialFailure(err) {
		t.Fatalf("IsCredentialFailure = false for %v", err)
	}
}
