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
	"net/netip"
	"strings"

	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type Config struct {
	Config string
	Secret string

	FailoverIPs []netip.Addr `yaml:"failoverIPs"`

	tokensrc oauth2.TokenSource
}

func (c *Config) Initialize(ctx context.Context, client kubernetes.Interface) error {
	conf := &oauth2.Config{
		ClientID: "scp",
		Scopes:   []string{"offline_access", "openid"},
		Endpoint: oauth2.Endpoint{
			AuthURL:       "https://www.servercontrolpanel.de/realms/scp/protocol/openid-connect/auth",
			DeviceAuthURL: "https://www.servercontrolpanel.de/realms/scp/protocol/openid-connect/auth/device",
			TokenURL:      "https://www.servercontrolpanel.de/realms/scp/protocol/openid-connect/token",
		},
	}

	if c.Config != "" {
		name, namespace, _ := strings.Cut(c.Config, "@")
		config, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		configData := make(map[string]any, len(config.Data))
		for key, value := range config.Data {
			var parsed any
			if err = yaml.Unmarshal([]byte(value), &parsed); err != nil {
				return err
			}
			configData[key] = parsed
		}
		data, err := yaml.Marshal(configData)
		if err != nil {
			return err
		}
		if err = yaml.Unmarshal(data, c); err != nil {
			return err
		}
	}

	if c.Secret != "" {
		name, namespace, _ := strings.Cut(c.Secret, "@")
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if token, ok := secret.Data["token"]; ok {
			var t oauth2.Token
			err = json.Unmarshal(token, &t)
			if err != nil {
				return err
			}
			c.tokensrc = conf.TokenSource(ctx, &t)
			klog.Infof("Retrieved access token from secret %s: %s", c.Secret, t.Expiry.Format("2006-01-02 15:04:05"))
		}
	}

	if c.tokensrc != nil {
		_, err := c.tokensrc.Token()
		if err != nil {
			c.tokensrc = nil
			klog.Warningf("Failed to use existing access token: %s", err)
		}
	}

	if c.tokensrc == nil {
		response, err := conf.DeviceAuth(ctx)
		if err != nil {
			return err
		}

		klog.Infof("To authenticate, visit %s and enter the code: %s", response.VerificationURIComplete, response.UserCode)

		token, err := conf.DeviceAccessToken(ctx, response)
		if err != nil {
			return err
		}

		klog.Infof("Successfully authenticated, access token expires at %s", token.Expiry.Format("2006-01-02 15:04:05"))

		klog.Infof("Secret name for storing access token: %s", c.Secret)
		if c.Secret != "" {
			klog.Infof("Storing access token in secret %s: %s", c.Secret, token.Expiry.Format("2006-01-02 15:04:05"))
			name, namespace, _ := strings.Cut(c.Secret, "@")
			secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			data, err := json.Marshal(token)
			if err != nil {
				return err
			}
			secret.Data["token"] = data
			secret, err = client.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		}

		c.tokensrc = conf.TokenSource(ctx, token)
	}

	return nil
}

func (c *Config) TokenSource() oauth2.TokenSource {
	return c.tokensrc
}

func (c *Config) IsFailoverAddr(ip netip.Addr) bool {
	for _, failover := range c.FailoverIPs {
		if failover == ip {
			return true
		}
	}
	return false
}
