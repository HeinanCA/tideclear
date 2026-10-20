package eksinfo

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/smithy-go"
)

var errExpiredSSO = errors.New("the SSO session associated with this profile has expired or is otherwise invalid")

func failingProvider() aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errExpiredSSO
	})
}

// deadHTTP fails every request: if credentials resolved, the error would be this one.
type deadHTTP struct{}

func (deadHTTP) Do(*http.Request) (*http.Response, error) { return nil, errors.New("network down") }

// realClientErr is the error a real EKS client returns when its provider fails.
func realClientErr(t *testing.T, p aws.CredentialsProvider) error {
	t.Helper()
	c := eks.New(eks.Options{Region: "us-east-1", Credentials: p, HTTPClient: deadHTTP{}, RetryMaxAttempts: 1})
	_, err := c.ListInsights(context.Background(), &eks.ListInsightsInput{ClusterName: aws.String("c")})
	if err == nil {
		t.Fatal("ListInsights succeeded offline")
	}
	return err
}

func TestWrappedProviderFailureIsCredentialFailure(t *testing.T) {
	err := realClientErr(t, WrapCredentials(failingProvider()))
	if !IsCredentialFailure(err) {
		t.Fatalf("IsCredentialFailure = false for %v", err)
	}
	var cre *CredentialRetrievalError
	if !errors.As(err, &cre) || !errors.Is(err, errExpiredSSO) {
		t.Fatalf("typed error or cause lost: %v", err)
	}
}

// Documents why WrapCredentials exists: the SDK itself does not tag the failure.
func TestUnwrappedProviderFailureIsNotRecognised(t *testing.T) {
	if err := realClientErr(t, failingProvider()); IsCredentialFailure(err) {
		t.Fatalf("SDK now tags provider failures itself; WrapCredentials may be redundant: %v", err)
	}
}

func TestWrapCredentialsPassesSuccessThrough(t *testing.T) {
	want := aws.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	p := WrapCredentials(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return want, nil }))
	if got, err := p.Retrieve(context.Background()); err != nil || got != want {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if WrapCredentials(nil) != nil {
		t.Fatal("nil provider must stay nil (anonymous)")
	}
}

func TestListCredentialRetrievalFailureIsNeverSkipped(t *testing.T) {
	// Even a provider error that looks like an EKS AccessDenied must not skip the region.
	causes := []error{errExpiredSSO, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}}
	for _, cause := range causes {
		bad := region(nil)
		bad.err = &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListClusters",
			Err: &CredentialRetrievalError{Err: cause}}
		good := region(map[string]string{"x": "1.35"}, "x")
		_, sk, err := lister(map[string]*fakeAPI{"b": bad, "g": good}).List(context.Background(), []string{"b", "g"}, "")
		if err == nil || len(sk) != 0 || !IsCredentialFailure(err) {
			t.Fatalf("cause %v: sk=%v err=%v, want fatal credential failure", cause, sk, err)
		}
		if IsAccessDenied(bad.err) || isAuthError(bad.err) {
			t.Fatalf("cause %v: retrieval failure classified as permission/auth-skippable", cause)
		}
	}
}

func TestListCredentialTimeoutIsNeverSkipped(t *testing.T) {
	bad := region(nil)
	bad.err = &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListClusters",
		Err: &CredentialRetrievalError{Err: context.DeadlineExceeded}}
	good := region(map[string]string{"x": "1.35"}, "x")
	_, sk, err := lister(map[string]*fakeAPI{"b": bad, "g": good}).List(context.Background(), []string{"b", "g"}, "")
	if err == nil || len(sk) != 0 || !IsCredentialFailure(err) {
		t.Fatalf("sk=%v err=%v, want fatal credential failure", sk, err)
	}
}
