package nacos

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

var _ config.Source = (*configSource)(nil)

// NewConfigSource creates a kratos config source that reads the given data ID
// from the Nacos config center and watches it for changes. The format is
// derived from the data ID extension (e.g. "user_center.yaml" -> yaml);
// missing extensions fall back to yaml.
func NewConfigSource(o Options, dataID string) (config.Source, error) {
	scs, err := o.serverConfigs()
	if err != nil {
		return nil, err
	}
	cc := o.clientConfig()
	cli, err := clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &cc,
		ServerConfigs: scs,
	})
	if err != nil {
		return nil, err
	}
	return &configSource{
		cli: cli,
		param: vo.ConfigParam{
			DataId: dataID,
			Group:  o.GroupName(),
		},
		format: formatOf(dataID),
	}, nil
}

type configSource struct {
	cli    config_client.IConfigClient
	param  vo.ConfigParam
	format string
}

// Load fetches the current config content from Nacos.
func (s *configSource) Load() ([]*config.KeyValue, error) {
	content, err := s.cli.GetConfig(s.param)
	if err != nil {
		return nil, err
	}
	// A missing or empty data ID should not shadow local files with an empty
	// document; skip it instead.
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}
	return []*config.KeyValue{{
		Key:    s.param.DataId,
		Value:  []byte(content),
		Format: s.format,
	}}, nil
}

// Watch subscribes to config changes through the Nacos listener API.
func (s *configSource) Watch() (config.Watcher, error) {
	return &watcher{
		cli:    s.cli,
		param:  s.param,
		format: s.format,
		notify: make(chan string, 1),
		stop:   make(chan struct{}),
	}, nil
}

var _ config.Watcher = (*watcher)(nil)

type watcher struct {
	cli    config_client.IConfigClient
	param  vo.ConfigParam
	format string

	notify    chan string
	stop      chan struct{}
	stopOnce  sync.Once
	mu        sync.Mutex
	listening bool
}

// Next blocks until the config changes in Nacos, then returns the new content.
func (w *watcher) Next() ([]*config.KeyValue, error) {
	w.mu.Lock()
	if !w.listening {
		w.listening = true
		param := w.param
		param.OnChange = func(_, _, _, data string) {
			select {
			case w.notify <- data:
			case <-w.stop:
			default:
				// A pending notification is already queued; drop this one.
			}
		}
		if err := w.cli.ListenConfig(param); err != nil {
			w.mu.Unlock()
			return nil, err
		}
	}
	w.mu.Unlock()

	select {
	case data := <-w.notify:
		if strings.TrimSpace(data) == "" {
			return nil, nil
		}
		return []*config.KeyValue{{
			Key:    w.param.DataId,
			Value:  []byte(data),
			Format: w.format,
		}}, nil
	case <-w.stop:
		return nil, context.Canceled
	}
}

// Stop cancels the Nacos listener and unblocks Next.
func (w *watcher) Stop() error {
	w.stopOnce.Do(func() { close(w.stop) })
	param := w.param
	param.OnChange = nil
	return w.cli.CancelListenConfig(param)
}

// formatOf derives the config format from the data ID extension.
func formatOf(dataID string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(dataID), ".")) {
	case "json":
		return "json"
	case "xml", "proto", "toml":
		return filepath.Ext(dataID)[1:]
	default:
		return "yaml"
	}
}
