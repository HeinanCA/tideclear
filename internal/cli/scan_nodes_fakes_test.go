package cli

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var nodeGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}

// nodegroupFake serves managed nodegroups; its zero value has none.
type nodegroupFake struct {
	groups []types.Nodegroup
	ngErr  error // returned by ListNodegroups
	ngHang bool  // ListNodegroups blocks until its context is done
}

func (f *nodegroupFake) ListNodegroups(ctx context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	if f.ngHang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.ngErr != nil {
		return nil, f.ngErr
	}
	var names []string
	for _, g := range f.groups {
		names = append(names, aws.ToString(g.NodegroupName))
	}
	return &eks.ListNodegroupsOutput{Nodegroups: names}, nil
}

func (f *nodegroupFake) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	for _, g := range f.groups {
		if aws.ToString(g.NodegroupName) == aws.ToString(in.NodegroupName) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &g}, nil
		}
	}
	return nil, errors.New("no such nodegroup")
}

func fakeNodegroup(name string, ami types.AMITypes, version, release string) types.Nodegroup {
	return types.Nodegroup{NodegroupName: aws.String(name), AmiType: ami, Version: aws.String(version), ReleaseVersion: aws.String(release)}
}

// fakeNode is a v1 Node as the API server returns it.
func fakeNode(name string, labels map[string]string, kubelet, osImage string) *unstructured.Unstructured {
	n := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": name},
		"status": map[string]any{"nodeInfo": map[string]any{"kubeletVersion": kubelet, "osImage": osImage}},
	}}
	n.SetLabels(labels)
	return n
}
