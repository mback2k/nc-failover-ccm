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
	"io"

	"github.com/carlmjohnson/versioninfo"
	"github.com/hooklift/gowsdl/soap"
	"github.com/mback2k/nc-failover-ccm/nc/scp"
	"gopkg.in/yaml.v3"

	"k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
)

const (
	providerName = "nc"

	scpWS = "https://www.servercontrolpanel.de/WSEndUser"
	xmlNS = "http://enduser.service.web.vcp.netcup.de/"
)

type cloud struct {
	config *Config
	client kubernetes.Interface
	server scp.WSEndUser
}

func (c *cloud) Initialize(ccb cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {
	c.client = ccb.ClientOrDie(providerName + "/" + versioninfo.Short())

	ctx, cancel := context.WithCancel(context.Background())
	go func(stop <-chan struct{}) { <-stop; cancel() }(stop)
	defer cancel()

	err := c.config.Initialize(ctx, c.client)
	if err != nil {
		panic(err)
	}

	c.server = scp.NewWSEndUser(soap.NewClient(scpWS))
}

func (c *cloud) Instances() (cloudprovider.Instances, bool) {
	// Replaced by InstancesV2
	return nil, false
}

func (c *cloud) InstancesV2() (cloudprovider.InstancesV2, bool) {
	return newInstancesV2(c), true
}

func (c *cloud) Zones() (cloudprovider.Zones, bool) {
	// Replaced by InstancesV2
	return nil, false
}

func (c *cloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	return newLoadBalancers(c), true
}

func (c *cloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

func (c *cloud) Routes() (cloudprovider.Routes, bool) {
	return nil, false
}

func (c *cloud) ProviderName() string {
	return providerName
}

func (c *cloud) HasClusterID() bool {
	return false
}

func (c *cloud) getServers(ctx context.Context) (*scp.GetVServersResponse, error) {
	req := &scp.GetVServers{
		XMLNS:     xmlNS,
		LoginName: c.config.Username,
		Password:  c.config.Password,
	}
	return c.server.GetVServersContext(ctx, req)
}

func (c *cloud) getServerState(ctx context.Context, serverName string) (*scp.GetVServerStateResponse, error) {
	req := &scp.GetVServerState{
		XMLNS:       xmlNS,
		LoginName:   c.config.Username,
		Password:    c.config.Password,
		VserverName: serverName,
	}
	return c.server.GetVServerStateContext(ctx, req)
}

func (c *cloud) getServerInfo(ctx context.Context, serverName string) (*scp.GetVServerInformationResponse, error) {
	req := &scp.GetVServerInformation{
		XMLNS:       xmlNS,
		LoginName:   c.config.Username,
		Password:    c.config.Password,
		Vservername: serverName,
	}
	return c.server.GetVServerInformationContext(ctx, req)
}

func (c *cloud) getServerIPs(ctx context.Context, serverName string) (*scp.GetVServerIPsResponse, error) {
	req := &scp.GetVServerIPs{
		XMLNS:       xmlNS,
		LoginName:   c.config.Username,
		Password:    c.config.Password,
		VserverName: serverName,
	}
	return c.server.GetVServerIPsContext(ctx, req)
}

func (c *cloud) routeServerIP(ctx context.Context, routedIP, routedMask, serverName, interfaceMAC string) (*scp.ChangeIPRoutingResponse, error) {
	req := &scp.ChangeIPRouting{
		XMLNS:                   xmlNS,
		LoginName:               c.config.Username,
		Password:                c.config.Password,
		RoutedIP:                routedIP,
		RoutedMask:              routedMask,
		DestinationVserverName:  serverName,
		DestinationInterfaceMAC: interfaceMAC,
	}
	return c.server.ChangeIPRoutingContext(ctx, req)
}

func newCloud(config io.Reader) (cloudprovider.Interface, error) {
	if config == nil {
		return nil, errors.New("missing cloud config file")
	}
	cfg := Config{}
	dec := yaml.NewDecoder(config)
	dec.KnownFields(true)
	err := dec.Decode(&cfg)
	return &cloud{config: &cfg}, err
}

func init() {
	cloudprovider.RegisterCloudProvider(providerName, newCloud)
}
