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

	"github.com/mback2k/nc-failover-ccm/nc/scpcore"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

type loadBalancers struct {
	cloud *cloud
}

func newLoadBalancers(cloud *cloud) *loadBalancers {
	return &loadBalancers{cloud}
}

func (l *loadBalancers) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (status *v1.LoadBalancerStatus, exists bool, err error) {
	klog.Infof("Querying loadbalancer status for service '%s'", service.Name)
	if service.Labels == nil {
		return nil, false, nil
	}
	if nodeName, ok := service.Labels[serviceNode]; ok {
		klog.Infof("Found existing loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
		needIPv4 := false
		needIPv6 := false
		for _, ipFamily := range service.Spec.IPFamilies {
			switch ipFamily {
			case v1.IPv4Protocol:
				needIPv4 = true
			case v1.IPv6Protocol:
				needIPv6 = true
			}
		}

		var failoverIPv4s []netip.Addr
		if needIPv4 {
			klog.Infof("Service '%s' requires IPv4", service.Name)
			failoverIPv4s, err = l.cloud.getFailoverIPv4s(ctx, &nodeName)
			if err != nil {
				return nil, false, err
			}
			if len(failoverIPv4s) == 0 {
				klog.Warningf("No failover IPv4 addresses found for node '%s'", nodeName)
				return nil, false, nil
			}
		}

		var failoverIPv6s []netip.Addr
		if needIPv6 {
			klog.Infof("Service '%s' requires IPv6", service.Name)
			failoverIPv6s, err = l.cloud.getFailoverIPv6s(ctx, &nodeName)
			if err != nil {
				return nil, false, err
			}
			if len(failoverIPv6s) == 0 {
				klog.Warningf("No failover IPv6 addresses found for node '%s'", nodeName)
				return nil, false, nil
			}
		}

		foundAll := true
		for _, ingress := range service.Status.LoadBalancer.Ingress {
			addr, err := netip.ParseAddr(ingress.IP)
			if err != nil {
				return nil, false, err
			}

			found := false
			if addr.Is4() {
				for _, ipv4 := range failoverIPv4s {
					klog.Infof("Node '%s' has IPv4 '%s'", nodeName, ipv4)
					if ipv4.String() == ingress.IP {
						klog.Infof("Found existing failover IPv4 '%s' on node '%s' for service '%s'", ipv4, nodeName, service.Name)
						needIPv4 = false
						found = true
						break
					}
				}
			} else if addr.Is6() {
				for _, ipv6 := range failoverIPv6s {
					klog.Infof("Node '%s' has IPv6 '%s'", nodeName, ipv6)
					if ipv6.String() == ingress.IP {
						klog.Infof("Found existing failover IPv6 '%s' on node '%s' for service '%s'", ipv6, nodeName, service.Name)
						needIPv6 = false
						found = true
						break
					}
				}
			}
			if !found {
				klog.Warningf("Existing loadbalancer for service '%s' on node '%s' is missing failover IP '%s'", service.Name, nodeName, ingress.IP)
				foundAll = false
			}
		}

		if foundAll && !needIPv4 && !needIPv6 {
			klog.Infof("Return existing loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
			return &service.Status.LoadBalancer, true, nil
		}
	}
	return nil, false, nil
}

func (l *loadBalancers) GetLoadBalancerName(ctx context.Context, clusterName string, service *v1.Service) string {
	klog.Infof("Querying loadbalancer name for service '%s'", service.Name)
	if service.Labels != nil {
		if nodeName, ok := service.Labels[serviceNode]; ok {
			klog.Infof("Found existing loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
			return nodeName
		}
	}
	return ""
}

func (l *loadBalancers) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	readyNodes := make(map[string]*v1.Node)
	for _, node := range nodes {
		for _, cond := range node.Status.Conditions {
			if cond.Type == v1.NodeReady && cond.Status == v1.ConditionTrue {
				readyNodes[node.Name] = node
				break
			}
		}
	}

	klog.Infof("Checking existing loadbalancer for service '%s'", service.Name)
	if service.Labels != nil {
		if nodeName, ok := service.Labels[serviceNode]; ok {
			if _, ok := readyNodes[nodeName]; ok {
				if status, exists, err := l.GetLoadBalancer(ctx, clusterName, service); exists {
					return status, err
				}
			}
		}
	}

	wantIPv4 := false
	wantIPv6 := false
	for _, ipFamily := range service.Spec.IPFamilies {
		switch ipFamily {
		case v1.IPv4Protocol:
			wantIPv4 = true
		case v1.IPv6Protocol:
			wantIPv6 = true
		}
	}

	klog.Infof("Searching matching loadbalancer for service '%s'", service.Name)
	for nodeName, node := range readyNodes {
		needIPv4 := wantIPv4
		needIPv6 := wantIPv6

		ingress := []v1.LoadBalancerIngress{}
		if needIPv4 {
			failoverIPv4s, err := l.cloud.getFailoverIPv4s(ctx, &nodeName)
			if err != nil {
				return nil, err
			}
			for _, ipv4 := range failoverIPv4s {
				klog.Infof("Found matching failover IPv4 '%s' on node '%s' for service '%s'", ipv4, nodeName, service.Name)
				ingress = append(ingress, v1.LoadBalancerIngress{IP: ipv4.String()})
				needIPv4 = false
				break
			}
		}

		if needIPv6 {
			failoverIPv6s, err := l.cloud.getFailoverIPv6s(ctx, &nodeName)
			if err != nil {
				return nil, err
			}
			for _, ipv6 := range failoverIPv6s {
				klog.Infof("Found matching failover IPv6 '%s' on node '%s' for service '%s'", ipv6, nodeName, service.Name)
				ingress = append(ingress, v1.LoadBalancerIngress{IP: ipv6.String()})
				needIPv6 = false
				break
			}
		}

		if !needIPv4 && !needIPv6 && len(ingress) > 0 {
			klog.Infof("Return matching loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
			return l.createLoadBalancerStatus(service, node, ingress)
		}
	}

	klog.Infof("Creating new loadbalancer for service '%s'", service.Name)
	for nodeName, node := range readyNodes {
		resp, err := l.cloud.getServer(ctx, nodeName, true)
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil || resp.JSON200.ServerLiveInfo == nil || resp.JSON200.Id == nil {
			klog.Warningf("Incomplete server information for node '%s', skipping loadbalancer creation on this node", nodeName)
			continue
		}
		if *resp.JSON200.ServerLiveInfo.State != scpcore.RUNNING {
			klog.Warningf("Server '%s' is not running, skipping loadbalancer creation on this node", nodeName)
			continue
		}

		needIPv4 := wantIPv4
		needIPv6 := wantIPv6
		ingress := []v1.LoadBalancerIngress{}

		var failoverIPv4s []netip.Addr
		if needIPv4 {
			klog.Infof("Service '%s' requires IPv4", service.Name)
			failoverIPv4s, err = l.cloud.getFailoverIPv4s(ctx, nil)
			if err != nil {
				return nil, err
			}
			if len(failoverIPv4s) == 0 {
				klog.Errorf("No failover IPv4 addresses found on any node")
				return nil, nil
			}
			for _, ipv4 := range failoverIPv4s {
				err := l.cloud.routeServerIP(ctx, ipv4, *resp.JSON200.Id)
				if err != nil {
					klog.Errorf("Failed to route failover IPv4 '%s' to node '%s' for service '%s': %v", ipv4, nodeName, service.Name, err)
					continue
				}
				klog.Infof("Rerouted failover IPv4 '%s' to node '%s' for service '%s'", ipv4, nodeName, service.Name)
				ingress = append(ingress, v1.LoadBalancerIngress{IP: ipv4.String()})
				needIPv4 = false
				break
			}
		}

		var failoverIPv6s []netip.Addr
		if needIPv6 {
			klog.Infof("Service '%s' requires IPv6", service.Name)
			failoverIPv6s, err = l.cloud.getFailoverIPv6s(ctx, nil)
			if err != nil {
				return nil, err
			}
			if len(failoverIPv6s) == 0 {
				klog.Errorf("No failover IPv6 addresses found on any node")
				return nil, nil
			}
			for _, ipv6 := range failoverIPv6s {
				err := l.cloud.routeServerIP(ctx, ipv6, *resp.JSON200.Id)
				if err != nil {
					klog.Errorf("Failed to route failover IPv6 '%s' to node '%s' for service '%s': %v", ipv6, nodeName, service.Name, err)
					continue
				}
				klog.Infof("Rerouted failover IPv6 '%s' to node '%s' for service '%s'", ipv6, nodeName, service.Name)
				ingress = append(ingress, v1.LoadBalancerIngress{IP: ipv6.String()})
				needIPv6 = false
				break
			}
		}

		if !needIPv4 && !needIPv6 && len(ingress) > 0 {
			klog.Infof("Created new loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
			return l.createLoadBalancerStatus(service, node, ingress)
		}
	}
	return nil, nil
}

func (l *loadBalancers) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	_, err := l.EnsureLoadBalancer(ctx, clusterName, service, nodes)
	return err
}

func (l *loadBalancers) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	if service.Labels != nil {
		if _, ok := service.Labels[serviceNode]; ok {
			return l.cloud.removeServiceNode(service, false)
		}
	}
	return nil
}

func (l *loadBalancers) createLoadBalancerStatus(service *v1.Service, node *v1.Node, ingress []v1.LoadBalancerIngress) (*v1.LoadBalancerStatus, error) {
	if service.Labels != nil {
		if _, ok := service.Labels[serviceNode]; ok {
			l.cloud.removeServiceNode(service, false)
		}
	}
	err := l.cloud.updateServiceNode(service, node)
	if err != nil {
		return nil, err
	}
	return &v1.LoadBalancerStatus{Ingress: ingress}, nil
}
