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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/carlmjohnson/versioninfo"
	"github.com/mback2k/nc-failover-ccm/nc/scpcore"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"

	"k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

const (
	providerName = "nc"
)

type cloud struct {
	config *Config
	client kubernetes.Interface

	scpcli scpcore.HttpRequestDoer
	scpapi *scpcore.ClientWithResponses

	userid int32
	server map[string]int32
}

func (c *cloud) Initialize(ccb cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {
	c.client = ccb.ClientOrDie(providerName + "/" + versioninfo.Short())
	c.server = make(map[string]int32)

	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) { <-done; cancel() }(stop)

	err := c.config.Initialize(ctx, c.client)
	if err != nil {
		panic(err)
	}

	c.scpcli = oauth2.NewClient(ctx, c.config.tokensrc)
	c.scpapi, err = scpcore.NewClientWithResponses("https://www.servercontrolpanel.de/scp-core",
		scpcore.WithHTTPClient(c.scpcli))
	if err != nil {
		panic(err)
	}

	userID, err := c.getUserID(ctx)
	if err != nil {
		panic(err)
	}
	klog.Infof("Cloud provider '%s' initialized with user ID %s", providerName, userID)
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

func (c *cloud) getUserID(ctx context.Context) (int32, error) {
	if c.userid != 0 {
		return c.userid, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.servercontrolpanel.de/realms/scp/protocol/openid-connect/userinfo", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.scpcli.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, errors.New(resp.Status)
	}
	var userInfo map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&userInfo)
	if err != nil {
		return 0, err
	}
	if id, ok := userInfo["id"].(string); ok {
		userID, err := strconv.Atoi(id)
		if err != nil {
			return 0, err
		}
		c.userid = int32(userID)
		return c.userid, nil
	}
	tasks, err := c.scpapi.GetApiV1TasksWithResponse(ctx, &scpcore.GetApiV1TasksParams{})
	if err != nil {
		return 0, err
	}
	if tasks.StatusCode() != http.StatusOK {
		return 0, errors.New(tasks.Status())
	}
	for _, task := range *tasks.JSON200 {
		if task.ExecutingUser.Id != nil && *task.ExecutingUser.Id != 0 {
			c.userid = *task.ExecutingUser.Id
			return c.userid, nil
		}
	}
	return 0, errors.New("user ID not found")
}

func (c *cloud) getServerID(ctx context.Context, serverName string) (int32, error) {
	serverID, exists := c.server[serverName]
	if exists {
		return serverID, nil
	}
	resp, err := c.scpapi.GetApiV1ServersWithResponse(ctx, &scpcore.GetApiV1ServersParams{
		Name: &serverName,
	})
	if err != nil {
		return 0, err
	}
	if resp.StatusCode() != http.StatusOK {
		return 0, errors.New(resp.Status())
	}
	for _, server := range *resp.JSON200 {
		if *server.Name == serverName && server.Id != nil {
			c.server[serverName] = *server.Id
			return c.server[serverName], nil
		}
	}
	return 0, errors.New("server ID not found")
}

func (c *cloud) getServer(ctx context.Context, serverName string, liveInfo bool) (*scpcore.GetApiV1ServersServerIdResponse, error) {
	serverID, err := c.getServerID(ctx, serverName)
	if err != nil {
		return nil, err
	}
	resp, err := c.scpapi.GetApiV1ServersServerIdWithResponse(ctx, serverID, &scpcore.GetApiV1ServersServerIdParams{
		LoadServerLiveInfo: &liveInfo,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, errors.New(resp.Status())
	}
	return resp, nil
}

func (c *cloud) getFailoverIPv4s(ctx context.Context, serverName *string) ([]netip.Addr, error) {
	userID, err := c.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	var serverId *int32
	if serverName != nil {
		serverID, err := c.getServerID(ctx, *serverName)
		if err != nil {
			return nil, err
		}
		serverId = &serverID
	}
	var failoverIPv4s []netip.Addr
	ipv4s, err := c.scpapi.GetApiV1UsersUserIdFailoveripsV4WithResponse(ctx, userID,
		&scpcore.GetApiV1UsersUserIdFailoveripsV4Params{ServerId: serverId})
	if err != nil {
		return nil, err
	}
	if ipv4s.StatusCode() != http.StatusOK {
		return nil, errors.New(ipv4s.Status())
	}
	for _, ipv4 := range *ipv4s.JSON200 {
		addr, err := netip.ParseAddr(*ipv4.Ip)
		if err != nil {
			return nil, err
		}
		if addr.Is4() {
			failoverIPv4s = append(failoverIPv4s, addr)
		}
	}
	return failoverIPv4s, nil
}

func (c *cloud) getFailoverIPv6s(ctx context.Context, serverName *string) ([]netip.Addr, error) {
	userID, err := c.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	var serverId *int32
	if serverName != nil {
		serverID, err := c.getServerID(ctx, *serverName)
		if err != nil {
			return nil, err
		}
		serverId = &serverID
	}
	var failoverIPv6s []netip.Addr
	ipv6s, err := c.scpapi.GetApiV1UsersUserIdFailoveripsV6WithResponse(ctx, userID,
		&scpcore.GetApiV1UsersUserIdFailoveripsV6Params{ServerId: serverId})
	if err != nil {
		return nil, err
	}
	if ipv6s.StatusCode() != http.StatusOK {
		return nil, errors.New(ipv6s.Status())
	}
	for _, ipv6 := range *ipv6s.JSON200 {
		addr, err := netip.ParseAddr(*ipv6.NetworkPrefix)
		if err != nil {
			return nil, err
		}
		if addr.Is6() {
			failoverIPv6s = append(failoverIPv6s, addr)
		}
	}
	return failoverIPv6s, nil
}

func (c *cloud) routeServerIP(ctx context.Context, addr netip.Addr, serverID int32) error {
	userID, err := c.getUserID(ctx)
	if err != nil {
		return err
	}
	if addr.Is4() {
		ipstr := addr.String()
		ipv4s, err := c.scpapi.GetApiV1UsersUserIdFailoveripsV4WithResponse(ctx, userID,
			&scpcore.GetApiV1UsersUserIdFailoveripsV4Params{Ip: &ipstr})
		if err != nil {
			return err
		}
		if ipv4s.StatusCode() != http.StatusOK {
			return errors.New(ipv4s.Status())
		}
		for _, ipv4 := range *ipv4s.JSON200 {
			if ipv4.Id == nil || ipv4.Ip == nil {
				klog.Warningf("Incomplete failover IPv4 information for IP '%s', skipping routing this IP to server ID %d", ipstr, serverID)
				continue
			}
			if *ipv4.Ip == ipstr {
				resp, err := c.scpapi.PatchApiV1UsersUserIdFailoveripsV4IdWithResponse(ctx, userID, *ipv4.Id,
					scpcore.PatchApiV1UsersUserIdFailoveripsV4IdJSONRequestBody{ServerId: &serverID})
				if err != nil {
					return err
				}
				if resp.StatusCode() != http.StatusAccepted {
					return errors.New(resp.Status())
				}
				return nil
			}
		}
	}
	if addr.Is6() {
		ipstr := addr.String()
		ipv6s, err := c.scpapi.GetApiV1UsersUserIdFailoveripsV6WithResponse(ctx, userID,
			&scpcore.GetApiV1UsersUserIdFailoveripsV6Params{Ip: &ipstr})
		if err != nil {
			return err
		}
		if ipv6s.StatusCode() != http.StatusOK {
			return errors.New(ipv6s.Status())
		}
		for _, ipv6 := range *ipv6s.JSON200 {
			if ipv6.Id == nil || ipv6.NetworkPrefix == nil {
				klog.Warningf("Incomplete failover IPv6 information for IP '%s', skipping routing this IP to server ID %d", ipstr, serverID)
				continue
			}
			if *ipv6.NetworkPrefix == ipstr {
				resp, err := c.scpapi.PatchApiV1UsersUserIdFailoveripsV6IdWithResponse(ctx, userID, *ipv6.Id,
					scpcore.PatchApiV1UsersUserIdFailoveripsV6IdJSONRequestBody{ServerId: &serverID})
				if err != nil {
					return err
				}
				if resp.StatusCode() != http.StatusAccepted {
					return errors.New(resp.Status())
				}
				return nil
			}
		}
	}
	return errors.New("failover IP not found")
}

func newCloud(config io.Reader) (cloudprovider.Interface, error) {
	if config == nil {
		return nil, errors.New("missing cloud config file")
	}
	cfg := Config{}
	dec := yaml.NewDecoder(config)
	dec.KnownFields(false)
	err := dec.Decode(&cfg)
	return &cloud{config: &cfg}, err
}

func init() {
	cloudprovider.RegisterCloudProvider(providerName, newCloud)
}
