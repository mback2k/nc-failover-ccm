/*
Copyright 2024 Marc Hörsken

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nc

import (
	"context"
	"net/netip"
	"slices"
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	cloudprovider "k8s.io/cloud-provider"
	cloudproviderapi "k8s.io/cloud-provider/api"
	"k8s.io/klog/v2"
)

const (
	serverStateOffline = "offline"
)

type instancesV2 struct {
	cloud *cloud
}

func newInstancesV2(cloud *cloud) *instancesV2 {
	return &instancesV2{cloud}
}

func (i *instancesV2) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	klog.Infof("Checking if server '%s' exists", node.Name)
	resp, err := i.cloud.getServers(ctx)
	if err != nil {
		return false, err
	}
	for _, name := range resp.Return_ {
		if *name == node.Name {
			klog.Infof("Server '%s' found", node.Name)
			return true, nil
		}
	}
	klog.Warningf("Server '%s' NOT found", node.Name)
	return false, nil
}

func (i *instancesV2) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	klog.Infof("Checking if server '%s' is shutdown", node.Name)
	resp, err := i.cloud.getServerState(ctx, node.Name)
	if err != nil {
		return false, err
	}
	klog.Infof("Server '%s' is '%s'", node.Name, resp.Return_)
	if resp.Return_ == serverStateOffline {
		return true, i.handleShutdown(ctx, node)
	}
	return false, nil
}

func (i *instancesV2) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	klog.Infof("Querying information for server '%s'", node.Name)
	resp, err := i.cloud.getServerInfo(ctx, node.Name)
	if err != nil {
		return nil, err
	}
	addresses := node.Status.Addresses
	for _, ip := range resp.Return_.Ips {
		if strings.ContainsRune(*ip, '/') {
			// Strip CIDR notation if present
			*ip, _, _ = strings.Cut(*ip, "/")
		}
		addr, err := netip.ParseAddr(*ip)
		if err != nil {
			return nil, err
		}
		if i.cloud.config.IsFailoverIP(addr) {
			klog.Infof("Skipping node '%s' failover IP: %s", node.Name, *ip)
			continue
		}
		address := v1.NodeAddress{
			Type:    v1.NodeExternalIP,
			Address: addr.String(),
		}
		if !slices.Contains(addresses, address) {
			klog.Infof("Adding node '%s' external IP: %s", node.Name, address.Address)
			addresses = append(addresses, address)
		}
	}
	providedNodeIP, exists := node.ObjectMeta.Annotations[cloudproviderapi.AnnotationAlphaProvidedIPAddr]
	if exists {
		for ip := range strings.SplitSeq(providedNodeIP, ",") {
			address := v1.NodeAddress{
				Type:    v1.NodeInternalIP,
				Address: ip,
			}
			if !slices.Contains(addresses, address) {
				klog.Infof("Adding node '%s' internal IP: %s", node.Name, address.Address)
				addresses = append(addresses, address)
			}
		}
	}
	klog.Infof("Server '%s' has addresses: %s", node.Name, addresses)
	providerID := i.cloud.ProviderName() + "://" + resp.Return_.VServerName
	return &cloudprovider.InstanceMetadata{ProviderID: providerID, NodeAddresses: addresses}, nil
}

func (i *instancesV2) handleShutdown(ctx context.Context, node *v1.Node) error {
	selector, err := labels.ValidatedSelectorFromSet(
		map[string]string{serviceNode: node.Name},
	)
	if err != nil {
		return err
	}
	core := i.cloud.client.CoreV1()
	opts := metav1.ListOptions{LabelSelector: selector.String()}
	services, err := core.Services("").List(ctx, opts)
	if err != nil {
		return err
	}
	for _, service := range services.Items {
		err := i.cloud.removeServiceNode(&service, true)
		if err != nil {
			return err
		}
	}
	return nil
}
