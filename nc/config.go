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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type Config struct {
	Config   string
	Secret   string
	Username string
	Password string
	Failover []string
	prefixes []netip.Prefix
}

func (c *Config) Initialize(ctx context.Context, client kubernetes.Interface) error {
	if c.Config != "" {
		name, namespace, _ := strings.Cut(c.Config, "@")
		config, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if username, ok := config.Data["username"]; ok {
			c.Username = username
		}
		if failover, ok := config.Data["failover"]; ok {
			c.Failover = strings.Split(failover, ",")
		}
	}
	if c.Secret != "" {
		name, namespace, _ := strings.Cut(c.Secret, "@")
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if username, ok := secret.Data["username"]; ok {
			c.Username = string(username)
		}
		if password, ok := secret.Data["password"]; ok {
			c.Password = string(password)
		}
	}
	if c.Username == "" {
		return errors.New("missing cloud username")
	}
	if c.Password == "" {
		return errors.New("missing cloud password")
	}
	if len(c.Failover) == 0 {
		return errors.New("missing cloud failover")
	}
	for _, failover := range c.Failover {
		prefix, err := netip.ParsePrefix(failover)
		if err != nil {
			return err
		}
		c.prefixes = append(c.prefixes, prefix)
		klog.Infof("Taking control of failover IP: %s", prefix.String())
	}
	return nil
}

func (c *Config) IsFailoverIP(addr netip.Addr) bool {
	for _, prefix := range c.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
