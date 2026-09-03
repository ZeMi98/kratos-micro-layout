package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"kratos-micro-layout/app/gateway/internal/conf"
	"kratos-micro-layout/app/gateway/internal/server"
	"kratos-micro-layout/pkg/log"
	"kratos-micro-layout/pkg/nacos"

	"github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
	klog "github.com/go-kratos/kratos/v3/log"
	_ "go.uber.org/automaxprocs"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	// Name is the name of the compiled software.
	Name string
	// Version is the version of the compiled software.
	Version string
	// flagconf is the config flag.
	flagconf string

	id, _ = os.Hostname()

	// nacosConfigDataID is the Nacos data ID the gateway reads its remote
	// config from. It matches the local file name in configs/.
	nacosConfigDataID = "gateway.yaml"
)

func init() {
	// Point at the gateway's own file (not the configs/ directory): every
	// service in the monorepo keeps its config there, and loading the whole
	// directory would merge sibling configs on top of this one.
	flag.StringVar(&flagconf, "conf", "../../../../configs/gateway.yaml", "config path, eg: -conf config.yaml")
}

// newLogger builds the slog-backed logger from config. kratos context
// handling and otel trace attrs are layered on top of it in main().
func newLogger(c *conf.Log) (*slog.Logger, func(), error) {
	return log.New(log.Options{
		Level:     c.GetLevel(),
		Format:    c.GetFormat(),
		Output:    c.GetOutput(),
		FilePath:  c.GetFilePath(),
		AddSource: true,
	})
}

// loadConfig loads the bootstrap config. Local files and env vars always
// provide the base; when registry.address points at Nacos, the config-center
// source is appended so remote values override local ones and hot-reload.
func loadConfig() (config.Config, *conf.Bootstrap, error) {
	local := []config.Source{
		file.NewSource(flagconf),
		env.NewSource("KRATOS"),
	}

	// Phase 1: read the local bootstrap to learn the Nacos settings.
	c := config.New(config.WithSource(local...))
	if err := c.Load(); err != nil {
		return nil, nil, err
	}
	var bc conf.Bootstrap
	if err := c.Scan(&bc); err != nil {
		return nil, nil, err
	}
	_ = c.Close()

	// Phase 2: append the Nacos config center only when an address is set. The
	// gateway requires discovery, so main() rejects an empty address later —
	// but loadConfig still tolerates it here so the failure message is clear.
	sources := local
	if addr := bc.Registry.GetAddress(); addr != "" {
		ns, err := nacos.NewConfigSource(nacos.Options{
			Address:     addr,
			NamespaceID: bc.Registry.GetNamespaceId(),
			Group:       bc.Registry.GetGroup(),
			Username:    bc.Registry.GetUsername(),
			Password:    bc.Registry.GetPassword(),
		}, nacosConfigDataID)
		if err != nil {
			return nil, nil, fmt.Errorf("create nacos config source: %w", err)
		}
		sources = append(sources, ns)
	}
	c = config.New(config.WithSource(sources...))
	if err := c.Load(); err != nil {
		return nil, nil, fmt.Errorf("load config (is registry.address pointing at a reachable Nacos server?): %w", err)
	}
	bc = conf.Bootstrap{}
	if err := c.Scan(&bc); err != nil {
		return nil, nil, err
	}
	return c, &bc, nil
}

func main() {
	flag.Parse()

	c, bc, err := loadConfig()
	if err != nil {
		panic(err)
	}
	defer c.Close()

	logger, cleanupLog, err := newLogger(bc.Log)
	if err != nil {
		panic(err)
	}
	defer cleanupLog()

	// Decorate the engine with kratos context attrs and otel trace ids.
	logger = klog.NewLogger(logger.Handler(), klog.WithExtractor(tracing.TraceAttrs)).With(
		slog.String("service.id", id),
		slog.String("service.name", Name),
		slog.String("service.version", Version),
	)
	klog.SetDefault(logger)

	// The gateway only routes requests to backends, so unlike the services it
	// never registers itself — but it cannot start without discovery. Nacos is
	// the sole backend: pkg/nacos builds the kratos discovery from the config.
	if bc.Registry.GetAddress() == "" {
		logger.Error("gateway requires service discovery: set registry.address to your Nacos server")
		os.Exit(1)
	}
	disc, err := nacos.NewRegistry(nacos.Options{
		Address:     bc.Registry.GetAddress(),
		NamespaceID: bc.Registry.GetNamespaceId(),
		Group:       bc.Registry.GetGroup(),
		Username:    bc.Registry.GetUsername(),
		Password:    bc.Registry.GetPassword(),
	})
	if err != nil {
		panic(fmt.Errorf("create nacos registry: %w", err))
	}

	hs, cleanupServer, err := server.New(bc.Server, bc.Gateway, bc.Middleware, disc, logger)
	if err != nil {
		panic(err)
	}
	defer cleanupServer()

	app := kratos.New(
		kratos.ID(id),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(map[string]string{}),
		kratos.Logger(logger),
		kratos.Server(hs),
	)
	// start and wait for stop signal
	if err := app.Run(); err != nil {
		panic(err)
	}
}
