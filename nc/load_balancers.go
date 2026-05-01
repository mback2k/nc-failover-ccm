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
		resp, err := l.cloud.getServer(ctx, nodeName, false)
		if err != nil {
			return nil, false, err
		}

		needIPv4 := false
		needIPv6 := false
		for _, ipFamily := range service.Spec.IPFamilies {
			if ipFamily == v1.IPv4Protocol {
				needIPv4 = true
			} else if ipFamily == v1.IPv6Protocol {
				needIPv6 = true
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
				for _, ipv4 := range *resp.JSON200.Ipv4Addresses {
					klog.Infof("Node '%s' has IPv4 '%s'", nodeName, *ipv4.Ip)
					if *ipv4.Ip == ingress.IP {
						klog.Infof("Found existing failover IPv4 '%s' on node '%s' for service '%s'", *ipv4.Ip, nodeName, service.Name)
						needIPv4 = false
						found = true
						break
					}
				}
			} else if addr.Is6() {
				for _, ipv6 := range *resp.JSON200.Ipv6Addresses {
					klog.Infof("Node '%s' has IPv6 '%s'", nodeName, *ipv6.NetworkPrefix)
					if *ipv6.NetworkPrefix == ingress.IP {
						klog.Infof("Found existing failover IPv6 '%s' on node '%s' for service '%s'", *ipv6.NetworkPrefix, nodeName, service.Name)
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
		if ipFamily == v1.IPv4Protocol {
			wantIPv4 = true
		} else if ipFamily == v1.IPv6Protocol {
			wantIPv6 = true
		}
	}

	klog.Infof("Searching matching loadbalancer for service '%s'", service.Name)
	for nodeName, node := range readyNodes {
		resp, err := l.cloud.getServer(ctx, nodeName, false)
		if err != nil {
			return nil, err
		}
		needIPv4 := wantIPv4
		needIPv6 := wantIPv6
		ingress := []v1.LoadBalancerIngress{}
		if needIPv4 {
			for _, ipv4 := range *resp.JSON200.Ipv4Addresses {
				klog.Infof("Node '%s' has IPv4 '%s'", nodeName, *ipv4.Ip)
				addr, err := netip.ParseAddr(*ipv4.Ip)
				if err != nil {
					return nil, err
				}
				if addr.Is4() && l.cloud.config.IsFailoverIP(addr) {
					klog.Infof("Found matching failover IPv4 '%s' on node '%s' for service '%s'", *ipv4.Ip, nodeName, service.Name)
					ingress = append(ingress, v1.LoadBalancerIngress{IP: addr.String()})
					needIPv4 = false
					break
				}
			}
		}
		if needIPv6 {
			for _, ipv6 := range *resp.JSON200.Ipv6Addresses {
				klog.Infof("Node '%s' has IPv6 '%s'", nodeName, *ipv6.NetworkPrefix)
				addr, err := netip.ParseAddr(*ipv6.NetworkPrefix)
				if err != nil {
					return nil, err
				}
				if addr.Is6() && l.cloud.config.IsFailoverIP(addr) {
					klog.Infof("Found matching failover IPv6 '%s' on node '%s' for service '%s'", *ipv6.NetworkPrefix, nodeName, service.Name)
					ingress = append(ingress, v1.LoadBalancerIngress{IP: addr.String()})
					needIPv6 = false
					break
				}
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
		if *resp.JSON200.ServerLiveInfo.State != scpcore.RUNNING {
			continue
		}
		needIPv4 := wantIPv4
		needIPv6 := wantIPv6
		ingress := []v1.LoadBalancerIngress{}
		for _, prefix := range l.cloud.config.prefixes {
			addr := prefix.Addr()
			if (addr.Is4() && !needIPv4) || (addr.Is6() && !needIPv6) {
				continue
			}
			ip := addr.String()
			err := l.cloud.routeServerIP(ctx, ip, *resp.JSON200.Id)
			if err != nil {
				return nil, err
			}
			klog.Infof("Rerouted failover IP '%s' to node '%s' for service '%s'", ip, nodeName, service.Name)
			ingress = append(ingress, v1.LoadBalancerIngress{IP: ip})
			if addr.Is4() {
				needIPv4 = false
			} else if addr.Is6() {
				needIPv6 = false
			}
			if !needIPv4 && !needIPv6 {
				break
			}
		}
		if len(ingress) > 0 {
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
