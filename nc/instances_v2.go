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
	"net/http"
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

const (
	serverStateOffline = "offline"
)

type instancesV2 struct {
	cloud *cloud

	cache map[string]int32
}

func newInstancesV2(cloud *cloud) *instancesV2 {
	return &instancesV2{cloud: cloud, cache: make(map[string]int32)}
}

func (i *instancesV2) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	klog.Infof("Checking if server '%s' exists", node.Name)
	resp, err := i.cloud.newapi.GetApiV1ServersWithResponse(ctx, &scpcore.GetApiV1ServersParams{
		Name: &node.Name,
	})
	if err != nil {
		return false, err
	}
	if resp.StatusCode() != http.StatusOK {
		return false, errors.New(resp.Status())
	}
	for _, server := range *resp.JSON200 {
		if *server.Name == node.Name && server.Id != nil {
			i.cache[node.Name] = *server.Id
			klog.Infof("Server '%s' found", node.Name)
			return true, nil
		}
	}
	klog.Warningf("Server '%s' NOT found", node.Name)
	return false, nil
}

func (i *instancesV2) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	klog.Infof("Checking if server '%s' is shutdown", node.Name)
	id, ok := i.cache[node.Name]
	if !ok {
		exists, err := i.InstanceExists(ctx, node)
		if !exists || err != nil {
			return false, err
		}
		id = i.cache[node.Name]
	}
	yes := true
	resp, err := i.cloud.newapi.GetApiV1ServersServerIdWithResponse(ctx, id, &scpcore.GetApiV1ServersServerIdParams{
		LoadServerLiveInfo: &yes,
	})
	if err != nil {
		return false, err
	}
	if resp.StatusCode() != http.StatusOK {
		return false, errors.New(resp.Status())
	}
	klog.Infof("Server '%s' is '%s'", node.Name, *resp.JSON200.ServerLiveInfo.State)
	if *resp.JSON200.ServerLiveInfo.State != scpcore.RUNNING {
		return true, i.handleShutdown(ctx, node)
	}
	return false, nil
}

func (i *instancesV2) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	klog.Infof("Querying information for server '%s'", node.Name)
	id, ok := i.cache[node.Name]
	if !ok {
		exists, err := i.InstanceExists(ctx, node)
		if !exists || err != nil {
			return nil, err
		}
		id = i.cache[node.Name]
	}
	no := false
	resp, err := i.cloud.newapi.GetApiV1ServersServerIdWithResponse(ctx, id, &scpcore.GetApiV1ServersServerIdParams{
		LoadServerLiveInfo: &no,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, errors.New(resp.Status())
	}
	addresses := node.Status.Addresses
	for _, ipv4 := range *resp.JSON200.Ipv4Addresses {
		klog.Infof("Server '%s' has IPv4 address: %s/%s", node.Name, *ipv4.Ip, *ipv4.Netmask)
		addr, err := netip.ParseAddr(*ipv4.Ip)
		if err != nil {
			return nil, err
		}
		if i.cloud.config.IsFailoverIP(addr) {
			klog.Infof("Skipping node '%s' failover IPv4: %s", node.Name, addr.String())
			continue
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
		if i.cloud.config.IsFailoverIP(addr) {
			klog.Infof("Skipping node '%s' failover IPv6: %s", node.Name, addr.String())
			continue
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
	providerID := i.cloud.ProviderName() + "://" + node.Name
	metadata := &cloudprovider.InstanceMetadata{
		ProviderID:    providerID,
		NodeAddresses: addresses,
		InstanceType:  resp.JSON200.Template.Name,
		Region:        resp.JSON200.Site.City,
		AdditionalLabels: map[string]string{
			"nc-failover-ccm.k8s.mback2k.net/server-id": strconv.FormatInt(int64(*resp.JSON200.Id), 10),
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
