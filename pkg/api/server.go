package api

import (
	normanapi "github.com/rancher/norman/api"
	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/settings"
)

// NOTE(JamLee): 创建 norman api 的 server。被引用为 rancherapi。一个被完整配置过的 norman server
func NewServer(schemas *types.Schemas) (*normanapi.Server, error) {
	server := normanapi.NewAPIServer()
	if err := server.AddSchemas(schemas); err != nil {
		return nil, err
	}
	server.CustomAPIUIResponseWriter(cssURL, jsURL, settings.APIUIVersion.Get)
	return server, nil
}

func cssURL() string {
	if settings.UIIndex.Get() != "local" {
		return ""
	}
	return "/api-ui/ui.min.css"
}

func jsURL() string {
	if settings.UIIndex.Get() != "local" {
		return ""
	}
	return "/api-ui/ui.min.js"
}
