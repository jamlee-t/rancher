package wrangler

import (
	"context"
	"github.com/rancher/rancher/pkg/features"
	"github.com/rancher/steve/pkg/accesscontrol"

	"github.com/rancher/rancher/pkg/wrangler/generated/controllers/management.cattle.io"
	managementv3 "github.com/rancher/rancher/pkg/wrangler/generated/controllers/management.cattle.io/v3"
	"github.com/rancher/remotedialer"
	"github.com/rancher/steve/pkg/server"
	"github.com/rancher/wrangler/pkg/apply"
	"github.com/rancher/wrangler/pkg/start"
	"k8s.io/client-go/rest"
)

// QUESTION(JamLee): wrangler 本身的用意是用于生成controller模板代码的，应该是个工具类，wrangler 也负责管理 controller 吗？
type Context struct {
	*server.Controllers

	Apply        apply.Apply
	Mgmt         managementv3.Interface
	TunnelServer *remotedialer.Server

	ASL      accesscontrol.AccessSetLookup
	starters []start.Starter
}

func (w *Context) Start(ctx context.Context) error {
	//  NOTE(JamLee): steve 里的 controller 是 wrangle-api 里面的 controller。
	if err := w.Controllers.Start(ctx); err != nil {
		return err
	}

	// NOTE(JamLee): starters 里面是rancher项目中 pkg/wrangle 包里的 controller。
	return start.All(ctx, 5, w.starters...)
}

func NewContext(ctx context.Context, restConfig *rest.Config, tunnelServer *remotedialer.Server) (*Context, error) {
	// NOTE(JamLee): 含有 wrangler-api 的 apiextensions 和 apiregistration 的 controller。主要是 k8s 扩展api的功能
	steveControllers, err := server.NewController(restConfig)
	if err != nil {
		return nil, err
	}

	// NOTE(JamLee): apply 似乎是一注入器，用于给结构体写入内容
	apply, err := apply.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	// NOTE(JamLee): 这里返回是 Factory， 意思是 Controller 的 Factory。可以从其中获取 wrangler 流派的 controller 。
	mgmt, err := management.NewFactoryFromConfig(restConfig)
	if err != nil {
		return nil, err
	}

	asl := accesscontrol.NewAccessStore(ctx, features.Steve.Enabled(), steveControllers.RBAC)

	return &Context{
		Controllers:  steveControllers,
		Apply:        apply,
		Mgmt:         mgmt.Management().V3(),
		TunnelServer: tunnelServer,
		ASL:          asl,
		starters: []start.Starter{
			mgmt,
		},
	}, nil
}
