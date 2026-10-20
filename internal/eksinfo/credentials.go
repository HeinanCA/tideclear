package eksinfo

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// CredentialRetrievalError marks a failure of the caller's credential provider
// (expired SSO session, no IMDS role, denied AssumeRole). The EKS client
// reports provider failures as plain wrapped errors, so WrapCredentials tags
// them at the source for IsCredentialFailure to find.
type CredentialRetrievalError struct{ Err error }

func (e *CredentialRetrievalError) Error() string { return "retrieve credentials: " + e.Err.Error() }

// Unwrap exposes the provider's own error.
func (e *CredentialRetrievalError) Unwrap() error { return e.Err }

// WrapCredentials returns p with every Retrieve error typed as a
// *CredentialRetrievalError. Wrap the provider that caches, so caching is kept.
func WrapCredentials(p aws.CredentialsProvider) aws.CredentialsProvider {
	if p == nil {
		return nil
	}
	return aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		creds, err := p.Retrieve(ctx)
		if err != nil {
			return aws.Credentials{}, &CredentialRetrievalError{Err: err}
		}
		return creds, nil
	})
}
