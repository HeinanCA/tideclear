package eksinfo

import (
	"errors"
	"fmt"
	"testing"

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
