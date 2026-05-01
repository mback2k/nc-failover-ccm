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
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/mback2k/nc-failover-ccm/nc/scpcore"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	cloudprovider "k8s.io/cloud-provider"
	cloudproviderapi "k8s.io/cloud-provider/api"
	"k8s.io/klog/v2"
)

type instancesV2 struct {
	cloud *cloud
}

func newInstancesV2(cloud *cloud) *instancesV2 {
	return &instancesV2{cloud: cloud}
}

func (i *instancesV2) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	klog.Infof("Checking if server '%s' exists", node.Name)
	resp, err := i.cloud.getServer(ctx, node.Name, false)
	if err != nil {
		return false, err
	}
	if resp.JSON200 == nil || resp.JSON200.Id == nil {
		return false, errors.New("server ID not found")
	}
	klog.Infof("Server '%s' found with ID %d", node.Name, *resp.JSON200.Id)
	return true, nil
}

func (i *instancesV2) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	klog.Infof("Checking if server '%s' is shutdown", node.Name)
	resp, err := i.cloud.getServer(ctx, node.Name, true)
	if err != nil {
		return false, err
	}
	if resp.JSON200 == nil || resp.JSON200.ServerLiveInfo == nil || resp.JSON200.ServerLiveInfo.State == nil {
		return false, errors.New("server live info not found")
	}
	klog.Infof("Server '%s' is '%s'", node.Name, *resp.JSON200.ServerLiveInfo.State)
	if *resp.JSON200.ServerLiveInfo.State != scpcore.RUNNING {
		return true, i.handleShutdown(ctx, node)
	}
	return false, nil
}

func (i *instancesV2) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	klog.Infof("Querying information for server '%s'", node.Name)
	resp, err := i.cloud.getServer(ctx, node.Name, false)
	if err != nil {
		return nil, err
	}
	if resp.JSON200 == nil || resp.JSON200.Ipv4Addresses == nil || resp.JSON200.Ipv6Addresses == nil ||
		resp.JSON200.Template == nil || resp.JSON200.Site == nil || resp.JSON200.Id == nil {
		return nil, errors.New("incomplete server information")
	}
	addresses := node.Status.Addresses
	for _, ipv4 := range *resp.JSON200.Ipv4Addresses {
		klog.Infof("Server '%s' has IPv4 address: %s/%s", node.Name, *ipv4.Ip, *ipv4.Netmask)
		addr, err := netip.ParseAddr(*ipv4.Ip)
		if err != nil {
			return nil, err
		}
		address := v1.NodeAddress{
			Type:    v1.NodeExternalIP,
			Address: addr.String(),
		}
		if !slices.Contains(addresses, address) {
			klog.Infof("Adding node '%s' external IPv4: %s", node.Name, address.Address)
			addresses = append(addresses, address)
		}
	}
	for _, ipv6 := range *resp.JSON200.Ipv6Addresses {
		klog.Infof("Server '%s' has IPv6 address: %s/%d", node.Name, *ipv6.NetworkPrefix, *ipv6.NetworkPrefixLength)
		addr, err := netip.ParseAddr(*ipv6.NetworkPrefix)
		if err != nil {
			return nil, err
		}
		address := v1.NodeAddress{
			Type:    v1.NodeExternalIP,
			Address: addr.String(),
		}
		if !slices.Contains(addresses, address) {
			klog.Infof("Adding node '%s' external IPv6: %s", node.Name, address.Address)
			addresses = append(addresses, address)
		}
	}
	providedNodeIP, exists := node.ObjectMeta.Annotations[cloudproviderapi.AnnotationAlphaProvidedIPAddr]
	if exists {
		for nodeIP := range strings.SplitSeq(providedNodeIP, ",") {
			klog.Infof("Server '%s' should have internal IP: %s", node.Name, nodeIP)
			addr, err := netip.ParseAddr(nodeIP)
			if err != nil {
				return nil, err
			}
			address := v1.NodeAddress{
				Type:    v1.NodeInternalIP,
				Address: addr.String(),
			}
			if !slices.Contains(addresses, address) {
				klog.Infof("Adding node '%s' internal IP: %s", node.Name, address.Address)
				addresses = append(addresses, address)
			}
		}
	}
	klog.Infof("Server '%s' has addresses: %s", node.Name, addresses)
	metadata := &cloudprovider.InstanceMetadata{
		ProviderID:    i.cloud.ProviderName() + "://" + *resp.JSON200.Name,
		NodeAddresses: addresses,
		InstanceType:  strings.ReplaceAll(resp.JSON200.Template.Name, " ", "_"),
		Region:        strings.ReplaceAll(resp.JSON200.Site.City, " ", "_"),
		AdditionalLabels: map[string]string{
			nodeUserID:   i.cloud.userid,
			nodeServerID: strconv.Itoa(int(*resp.JSON200.Id)),
		},
	}
	klog.Infof("Server '%s' has metadata: %v", node.Name, metadata)
	return metadata, nil
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
