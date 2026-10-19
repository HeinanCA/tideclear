package cli

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

func TestClientFactoryLoadsConfigOnceAndOverridesRegion(t *testing.T) {
	loads := 0
	f := clientFactory(func(profile, region string) (aws.Config, error) {
		loads++
		return aws.Config{Region: "from-profile"}, nil
	})
	for _, r := range []string{"eu-x", "us-y", "ap-z"} {
		api, err := f("prof", r)
		if err != nil {
			t.Fatal(err)
		}
		if got := api.(*eks.Client).Options().Region; got != r {
			t.Errorf("client region = %q, want %q", got, r)
		}
	}
	if loads != 1 {
		t.Errorf("config loaded %d times, want 1", loads)
	}
}

func TestClientFactoryConfigErrorIsPlain(t *testing.T) {
	f := clientFactory(func(string, string) (aws.Config, error) { return aws.Config{}, errors.New("bad profile") })
	if _, err := f("p", "r1"); err == nil || err.Error() != "bad profile" {
		t.Fatalf("err = %v", err)
	}
}

func TestClientFactoryPassesRegionToConfigLoad(t *testing.T) {
	var got []string
	f := clientFactory(func(profile, region string) (aws.Config, error) {
		got = append(got, region)
		return aws.Config{Region: region}, nil
	})
	for _, r := range []string{"eu-x", "us-y"} {
		if _, err := f("prof", r); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0] != "eu-x" {
		t.Errorf("load regions = %v, want [eu-x]", got)
	}
}
