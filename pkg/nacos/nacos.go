// Package nacos wraps the Nacos SDK into the small surface this template
// needs: a kratos Registrar/Discovery for service registration, and a kratos
// config.Source for the config center. Connection settings are shared through
// Options so every service wires Nacos the same way.
package nacos

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	nacosreg "github.com/go-kratos/kratos/contrib/registry/nacos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// Options describes a Nacos server connection shared by the naming client
// (registration/discovery) and the config client (config center).
type Options struct {
	// Address is one or more Nacos server endpoints as "host:port",
	// comma-separated for clusters.
	Address string
	// NamespaceID isolates registrations and configs. Empty uses the public
	// namespace.
	NamespaceID string
	// Group groups services and configs. Empty uses DEFAULT_GROUP.
	Group string
	// Username for Nacos auth. Optional.
	Username string
	// Password for Nacos auth. Optional.
	Password string
}

// GroupName returns the effective group, defaulting to DEFAULT_GROUP.
func (o Options) GroupName() string {
	if strings.TrimSpace(o.Group) == "" {
		return constant.DEFAULT_GROUP
	}
	return o.Group
}

// serverConfigs parses Address into nacos ServerConfigs.
func (o Options) serverConfigs() ([]constant.ServerConfig, error) {
	parts := strings.Split(o.Address, ",")
	scs := make([]constant.ServerConfig, 0, len(parts))
	for _, part := range parts {
		host, portStr, err := net.SplitHostPort(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("nacos: invalid address %q: %w", part, err)
		}
		var port uint64
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
			return nil, fmt.Errorf("nacos: invalid port in address %q: %w", part, err)
		}
		scs = append(scs, constant.ServerConfig{
			Scheme: "http",
			IpAddr: host,
			Port:   port,
		})
	}
	return scs, nil
}

// clientConfig builds the shared SDK client config. Cache and log dirs are
// redirected into the OS temp dir so services never litter their working
// directory.
func (o Options) clientConfig() constant.ClientConfig {
	return constant.ClientConfig{
		NamespaceId:         o.NamespaceID,
		Username:            o.Username,
		Password:            o.Password,
		NotLoadCacheAtStart: true,
		CacheDir:            filepath.Join(os.TempDir(), "nacos", "cache"),
		LogDir:              filepath.Join(os.TempDir(), "nacos", "log"),
	}
}

// NewNamingClient creates a naming client for service registration and
// discovery.
func NewNamingClient(o Options) (naming_client.INamingClient, error) {
	scs, err := o.serverConfigs()
	if err != nil {
		return nil, err
	}
	cc := o.clientConfig()
	return clients.NewNamingClient(vo.NacosClientParam{
		ClientConfig:  &cc,
		ServerConfigs: scs,
	})
}

// NewRegistry creates a Nacos-backed kratos registry. The returned value
// implements both registry.Registrar (server side) and registry.Discovery
// (client side), so one instance wires registration and lookup.
func NewRegistry(o Options) (*nacosreg.Registry, error) {
	cli, err := NewNamingClient(o)
	if err != nil {
		return nil, err
	}
	return nacosreg.New(cli, nacosreg.WithGroup(o.GroupName())), nil
}

// Registrar creates a Nacos-backed Registrar. Shorthand for NewRegistry when
// only registration is needed.
func Registrar(o Options) (registry.Registrar, error) {
	reg, err := NewRegistry(o)
	if err != nil {
		return nil, err
	}
	return reg, nil
}
