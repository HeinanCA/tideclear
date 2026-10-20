package eksinfo

import (
	"errors"
	"fmt"
	"testing"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

func TestIsAccessDenied(t *testing.T) {
	apiErr := func(code string) error {
		return &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
			Err: &smithy.GenericAPIError{Code: code, Message: "m"}}
	}
	nestedSTS := &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
		Err: fmt.Errorf("get credentials: %w", &smithy.OperationError{ServiceID: "STS", OperationName: "AssumeRole",
			Err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized"}})}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"AccessDeniedException", apiErr("AccessDeniedException"), true},
		{"AccessDenied", apiErr("AccessDenied"), true},
		{"SDK modeled", &types.AccessDeniedException{}, true},
		{"UnrecognizedClientException", apiErr("UnrecognizedClientException"), false},
		{"InvalidClientTokenId", apiErr("InvalidClientTokenId"), false},
		{"throttle", apiErr("ThrottlingException"), false},
		{"nested STS AccessDenied", nestedSTS, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsAccessDenied(c.err); got != c.want {
			t.Errorf("%s: IsAccessDenied = %v, want %v", c.name, got, c.want)
		}
	}
}

// signingErr is how the SDK reports a failed credential provider: a
// SigningError (aws/signer/v4 middleware) inside the EKS operation error.
func signingErr(cause error) error {
	return &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
		Err: &v4.SigningError{Err: fmt.Errorf("failed to retrieve credentials: %w", cause)}}
}

func TestIsCredentialFailure(t *testing.T) {
	apiErr := func(code string) error {
		return &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
			Err: &smithy.GenericAPIError{Code: code, Message: "m"}}
	}
	nestedSTS := &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
		Err: fmt.Errorf("get credentials: %w", &smithy.OperationError{ServiceID: "STS", OperationName: "AssumeRole",
			Err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized"}})}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"credential retrieval", nestedSTS, true},
		{"signing: expired SSO token", signingErr(errors.New("the SSO session has expired or is invalid")), true},
		{"signing: provider failure", signingErr(errors.New("no EC2 IMDS role found")), true},
		{"InvalidAccessKeyId", apiErr("InvalidAccessKeyId"), true},
		{"TokenRefreshRequired", apiErr("TokenRefreshRequired"), true},
		{"plain error mentioning credentials", errors.New("failed to retrieve credentials"), false},
		{"UnrecognizedClientException", apiErr("UnrecognizedClientException"), true},
		{"InvalidClientTokenId", apiErr("InvalidClientTokenId"), true},
		{"ExpiredToken", apiErr("ExpiredToken"), true},
		{"ExpiredTokenException", apiErr("ExpiredTokenException"), true},
		{"RequestExpired", apiErr("RequestExpired"), true},
		{"InvalidSignatureException", apiErr("InvalidSignatureException"), true},
		{"SignatureDoesNotMatch", apiErr("SignatureDoesNotMatch"), true},
		{"AuthFailure", apiErr("AuthFailure"), true},
		{"IncompleteSignature", apiErr("IncompleteSignature"), true},
		{"MissingAuthenticationToken", apiErr("MissingAuthenticationToken"), true},
		{"MissingAuthenticationTokenException", apiErr("MissingAuthenticationTokenException"), true},
		{"AccessDeniedException is a permission, not a credential", apiErr("AccessDeniedException"), false},
		{"throttle", apiErr("ThrottlingException"), false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsCredentialFailure(c.err); got != c.want {
			t.Errorf("%s: IsCredentialFailure = %v, want %v", c.name, got, c.want)
		}
	}
}
