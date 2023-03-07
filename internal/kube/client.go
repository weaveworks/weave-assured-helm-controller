/*
Copyright 2023 The Flux authors

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

package kube

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/fluxcd/pkg/runtime/client"
)

// Option is a function that configures an MemoryRESTClientGetter.
type Option func(*MemoryRESTClientGetter)

// WithNamespace sets the namespace to use for the client.
func WithNamespace(namespace string) Option {
	return func(c *MemoryRESTClientGetter) {
		c.namespace = namespace
	}
}

// WithImpersonate sets the service account to impersonate. It configures the
// REST client to impersonate the service account in the given namespace, and
// sets the service account name as the username to use in the raw KubeConfig.
func WithImpersonate(serviceAccount, namespace string) Option {
	return func(c *MemoryRESTClientGetter) {
		if username := SetImpersonationConfig(c.cfg, namespace, serviceAccount); username != "" {
			c.impersonate = username
		}
	}
}

// WithClientOptions sets the client options (e.g. QPS and Burst) to use for
// the client.
func WithClientOptions(opts client.Options) Option {
	return func(c *MemoryRESTClientGetter) {
		c.cfg.Burst = opts.Burst
		c.cfg.QPS = opts.QPS
	}
}

// MemoryRESTClientGetter is a resource.RESTClientGetter that uses an
// in-memory REST config, REST mapper, and discovery client. The REST config,
// REST mapper, and discovery client are lazily initialized, and cached for
// subsequent calls.
type MemoryRESTClientGetter struct {
	// namespace is the namespace to use for the client.
	namespace string
	// impersonate is the username to use for the client.
	impersonate string

	cfg *rest.Config

	restMapper   meta.RESTMapper
	restMapperMu sync.Mutex

	discoveryClient discovery.CachedDiscoveryInterface
	discoveryMu     sync.Mutex

	clientCfg   clientcmd.ClientConfig
	clientCfgMu sync.Mutex
}

// setDefaults sets the default values for the MemoryRESTClientGetter.
func (c *MemoryRESTClientGetter) setDefaults() {
	if c.namespace == "" {
		c.namespace = "default"
	}
}

// NewMemoryRESTClientGetter returns a new MemoryRESTClientGetter.
func NewMemoryRESTClientGetter(cfg *rest.Config, opts ...Option) *MemoryRESTClientGetter {
	g := &MemoryRESTClientGetter{
		cfg: cfg,
	}
	for _, opts := range opts {
		opts(g)
	}
	g.setDefaults()
	return g
}

// NewInClusterMemoryRESTClientGetter returns a new MemoryRESTClientGetter
// that uses the in-cluster REST config. It returns an error if the in-cluster
// REST config cannot be obtained.
func NewInClusterMemoryRESTClientGetter(opts ...Option) (*MemoryRESTClientGetter, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get config for in-cluster REST client: %w", err)
	}
	return NewMemoryRESTClientGetter(cfg, opts...), nil
}

// ToRESTConfig returns the in-memory REST config.
func (c *MemoryRESTClientGetter) ToRESTConfig() (*rest.Config, error) {
	if c.cfg == nil {
		return nil, fmt.Errorf("MemoryRESTClientGetter has no REST config")
	}
	return c.cfg, nil
}

// ToDiscoveryClient returns a memory cached discovery client. Calling it
// multiple times will return the same instance.
func (c *MemoryRESTClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	c.clientCfgMu.Lock()
	defer c.clientCfgMu.Unlock()

	if c.discoveryClient == nil {
		config, err := c.ToRESTConfig()
		if err != nil {
			return nil, err
		}

		discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
		if err != nil {
			return nil, err
		}
		c.discoveryClient = memory.NewMemCacheClient(discoveryClient)
	}
	return c.discoveryClient, nil
}

// ToRESTMapper returns a meta.RESTMapper using the discovery client. Calling
// it multiple times will return the same instance.
func (c *MemoryRESTClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()

	if c.restMapper == nil {
		discoveryClient, err := c.ToDiscoveryClient()
		if err != nil {
			return nil, err
		}
		mapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
		c.restMapper = restmapper.NewShortcutExpander(mapper, discoveryClient)
	}
	return c.restMapper, nil
}

// ToRawKubeConfigLoader returns a clientcmd.ClientConfig using
// clientcmd.DefaultClientConfig. With clientcmd.ClusterDefaults, namespace, and
// impersonate configured as overwrites.
func (c *MemoryRESTClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	c.clientCfgMu.Lock()
	defer c.clientCfgMu.Unlock()

	if c.clientCfg == nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		// use the standard defaults for this client command
		// DEPRECATED: remove and replace with something more accurate
		loadingRules.DefaultClientConfig = &clientcmd.DefaultClientConfig

		overrides := &clientcmd.ConfigOverrides{ClusterDefaults: clientcmd.ClusterDefaults}
		overrides.Context.Namespace = c.namespace
		overrides.AuthInfo.Impersonate = c.impersonate

		c.clientCfg = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	}
	return c.clientCfg
}
