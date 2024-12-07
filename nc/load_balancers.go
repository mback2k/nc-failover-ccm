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
	"strconv"

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
	if nodeName, ok := service.Labels[serviceNode]; ok {
		klog.Infof("Found existing loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
		resp, err := l.cloud.getServerIPs(ctx, nodeName)
		if err != nil {
			return nil, false, err
		}

		foundAll := true
		for _, ingress := range service.Status.LoadBalancer.Ingress {
			found := false
			for _, ip := range resp.Return_ {
				if *ip == ingress.IP {
					klog.Infof("Found failover IP '%s' on node '%s' for service '%s'", *ip, nodeName, service.Name)
					found = true
					break
				}
			}
			if !found {
				foundAll = false
			}
		}
		if foundAll {
			return &service.Status.LoadBalancer, true, nil
		}
	}
	return nil, false, nil
}

func (l *loadBalancers) GetLoadBalancerName(ctx context.Context, clusterName string, service *v1.Service) string {
	klog.Infof("Querying loadbalancer name for service '%s'", service.Name)
	if nodeName, ok := service.Labels[serviceNode]; ok {
		klog.Infof("Found existing loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
		return nodeName
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
	if nodeName, ok := service.Labels[serviceNode]; ok {
		if _, ok := readyNodes[nodeName]; ok {
			if status, exists, err := l.GetLoadBalancer(ctx, clusterName, service); exists {
				return status, err
			}
		}
	}

	klog.Infof("Searching existing loadbalancer for service '%s'", service.Name)
	for nodeName, node := range readyNodes {
		resp, err := l.cloud.getServerIPs(ctx, nodeName)
		if err != nil {
			return nil, err
		}
		ingress := []v1.LoadBalancerIngress{}
		for _, ip := range resp.Return_ {
			addr, err := netip.ParseAddr(*ip)
			if err != nil {
				return nil, err
			}
			if l.cloud.config.IsFailoverIP(addr) {
				ingress = append(ingress, v1.LoadBalancerIngress{IP: addr.String()})
			}
		}
		if len(ingress) > 0 {
			klog.Infof("Found existing loadbalancer for service '%s' on node '%s'", service.Name, nodeName)
			return l.createLoadBalancerStatus(service, node, ingress)
		}
	}

	klog.Infof("Creating new loadbalancer for service '%s'", service.Name)
	for nodeName, node := range readyNodes {
		resp, err := l.cloud.getServerInfo(ctx, nodeName)
		if err != nil {
			return nil, err
		}
		if resp.Return_.Status == serverStateOffline {
			continue
		}
		ingress := []v1.LoadBalancerIngress{}
		for _, iface := range resp.Return_.ServerInterfaces {
			/* identify public interface based upon existence of IPs */
			if len(iface.Ipv4IP) > 0 && len(iface.Ipv6IP) > 0 {
				assignedIPv4 := false
				assignedIPv6 := false
				for _, prefix := range l.cloud.config.prefixes {
					addr := prefix.Addr()
					if (assignedIPv4 && addr.Is4()) || (assignedIPv6 && addr.Is6()) {
						continue
					}
					ip := addr.String()
					resp, err := l.cloud.routeServerIP(ctx, ip, strconv.Itoa(prefix.Bits()), resp.Return_.VServerName, iface.Mac)
					if err != nil {
						return nil, err
					}
					if resp.Return_ {
						ingress = append(ingress, v1.LoadBalancerIngress{IP: ip})
						if addr.Is4() {
							assignedIPv4 = true
						} else if addr.Is6() {
							assignedIPv6 = true
						}
					}
					if assignedIPv4 && assignedIPv6 {
						break
					}
				}
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
	if _, ok := service.Labels[serviceNode]; ok {
		return l.cloud.removeServiceNode(service, false)
	}
	return nil
}

func (l *loadBalancers) createLoadBalancerStatus(service *v1.Service, node *v1.Node, ingress []v1.LoadBalancerIngress) (*v1.LoadBalancerStatus, error) {
	err := l.cloud.updateServiceNode(service, node)
	if err != nil {
		return nil, err
	}
	return &v1.LoadBalancerStatus{Ingress: ingress}, nil
}
