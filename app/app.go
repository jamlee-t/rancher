package app

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/rancher/norman/pkg/k8scheck"
	"github.com/rancher/rancher/pkg/audit"
	"github.com/rancher/rancher/pkg/auth/providerrefresh"
	"github.com/rancher/rancher/pkg/auth/providers/common"
	"github.com/rancher/rancher/pkg/auth/tokens"
	"github.com/rancher/rancher/pkg/clustermanager"
	managementController "github.com/rancher/rancher/pkg/controllers/management"
	"github.com/rancher/rancher/pkg/cron"
	"github.com/rancher/rancher/pkg/dialer"
	"github.com/rancher/rancher/pkg/features"
	"github.com/rancher/rancher/pkg/jailer"
	"github.com/rancher/rancher/pkg/metrics"
	"github.com/rancher/rancher/pkg/rbac"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/rancher/pkg/steve"
	"github.com/rancher/rancher/pkg/steve/pkg/clusterapi"
	"github.com/rancher/rancher/pkg/telemetry"
	"github.com/rancher/rancher/pkg/tls"
	"github.com/rancher/rancher/pkg/tunnelserver"
	"github.com/rancher/rancher/pkg/wrangler"
	"github.com/rancher/rancher/server"
	"github.com/rancher/remotedialer"
	"github.com/rancher/steve/pkg/accesscontrol"
	"github.com/rancher/steve/pkg/auth"
	steveserver "github.com/rancher/steve/pkg/server"
	"github.com/rancher/types/config"
	"github.com/rancher/wrangler/pkg/crd"
	"github.com/rancher/wrangler/pkg/leader"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"k8s.io/client-go/tools/clientcmd"
)

type Config struct {
	ACMEDomains       cli.StringSlice
	AddLocal          string
	Embedded          bool
	HTTPListenPort    int
	HTTPSListenPort   int
	K8sMode           string
	Debug             bool
	Trace             bool
	NoCACerts         bool
	AuditLogPath      string
	AuditLogMaxage    int
	AuditLogMaxsize   int
	AuditLogMaxbackup int
	AuditLevel        int
	Features          string
}

// NOTE(JamLee): 核心 app 对象  Rancher
type Rancher struct {
	Config          Config
	AccessSetLookup accesscontrol.AccessSetLookup
	Handler         http.Handler
	Auth            auth.Middleware
	ScaledContext   *config.ScaledContext
	WranglerContext *wrangler.Context
	ClusterManager  *clustermanager.Manager
}

// NOTE(JamLee): Rancher 的启动分为 New 和 Listen 两个阶段。Listen 阶段是用于启动端口监听, Controller start。
func (r *Rancher) ListenAndServe(ctx context.Context) error {
	if err := r.Start(ctx); err != nil {
		return err
	}

	if err := tls.ListenAndServe(ctx, &r.ScaledContext.RESTConfig,
		r.Handler,
		r.Config.HTTPSListenPort,
		r.Config.HTTPListenPort,
		r.Config.ACMEDomains,
		r.Config.NoCACerts); err != nil {
		return err
	}

	<-ctx.Done()
	return ctx.Err()
}

// NOTE(JamLee): New 阶段，安装 crd 到 k8s， 启动 scaledContext。features 是指 istio，dashboard 等可关闭的特性
func initFeatures(ctx context.Context, scaledContext *config.ScaledContext, cfg *Config) error {
	// NOTE(JamLee): wrangle 库中 factory 主要就是一个 clientset（k8s）。但是它有很多方法：比如批量创建 crd
	factory, err := crd.NewFactoryFromClient(&scaledContext.RESTConfig)
	if err != nil {
		return err
	}

	if _, err := factory.CreateCRDs(ctx, crd.NonNamespacedType("Feature.management.cattle.io/v3")); err != nil {
		return err
	}

	scaledContext.Management.Features("").Controller()
	if err := scaledContext.Start(ctx); err != nil {
		return err
	}
	features.InitializeFeatures(scaledContext, cfg.Features)
	return nil
}

// NOTE(JamLee): New阶段，这里其实不止 scaleConext, 还有  wrangler context
func buildScaledContext(ctx context.Context, clientConfig clientcmd.ClientConfig, cfg *Config) (*config.ScaledContext, *clustermanager.Manager, *wrangler.Context, error) {
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	restConfig.Timeout = 30 * time.Second

	kubeConfig, err := clientConfig.RawConfig()
	if err != nil {
		return nil, nil, nil, err
	}

	// NOTE(JamLee): 等待 k8s 启动完毕
	if err := k8scheck.Wait(ctx, *restConfig); err != nil {
		return nil, nil, nil, err
	}

	// NOTE(JamLee): config 是 types 中的包。与 app.Config 和 kubeConfig 无关。是一级 controller们的总代。
	scaledContext, err := config.NewScaledContext(*restConfig)
	if err != nil {
		return nil, nil, nil, err
	}
	scaledContext.KubeConfig = kubeConfig

	// NOTE(JamLee): 初始化自定义资源feature到 etcd 里。feature 连 schema 都没有, 用途是什么呢？
	if err := initFeatures(ctx, scaledContext, cfg); err != nil {
		return nil, nil, nil, err
	}

	dialerFactory, err := dialer.NewFactory(scaledContext)
	if err != nil {
		return nil, nil, nil, err
	}

	// NOTE(JamLee): PeerManager 与 rancher 的高可用集群有关系
	scaledContext.Dialer = dialerFactory
	scaledContext.PeerManager, err = tunnelserver.NewPeerManager(ctx, scaledContext, dialerFactory.TunnelServer)
	if err != nil {
		return nil, nil, nil, err
	}

	var tunnelServer *remotedialer.Server
	if df, ok := scaledContext.Dialer.(*dialer.Factory); ok {
		tunnelServer = df.TunnelServer
	}

	// NOTE(JamLee): wrangler 流派的 controller 。负责 cluster，user 两个资源的 k8s api。
	wranglerContext, err := wrangler.NewContext(ctx, steveserver.RestConfigDefaults(&scaledContext.RESTConfig), tunnelServer)
	if err != nil {
		return nil, nil, nil, err
	}

	// NOTE(JamLee): 用户登录授权
	userManager, err := common.NewUserManager(scaledContext)
	if err != nil {
		return nil, nil, nil, err
	}

	scaledContext.UserManager = userManager
	scaledContext.RunContext = ctx // NOTE(JamLee): 停止的命令

	// NOTE(JamLee): 多集群管理工具,
	manager := clustermanager.NewManager(cfg.HTTPSListenPort, scaledContext, wranglerContext.RBAC, wranglerContext.ASL)
	scaledContext.AccessControl = manager
	scaledContext.ClientGetter = manager

	return scaledContext, manager, wranglerContext, nil
}

// NOTE(JamLee): Rancher 的启动分为 New 和 Listen 两个阶段。New 阶段是用于组织对象，但是不启动。
func New(ctx context.Context, clientConfig clientcmd.ClientConfig, cfg *Config) (*Rancher, error) {
	// NOTE(JamLee): 什么是 ScaledContext 的概念 ？高可用时每个 managment 拥有一个 scaledContext。但是只有 master 有 managermentContext。
	//  里面存放的是 norman 浏览的 controller。
	//  什么是 wranglerContext 的概念？wranglerContext 是独立的一套wrangler流派的 controller。
	scaledContext, clusterManager, wranglerContext, err := buildScaledContext(ctx, clientConfig, cfg)
	if err != nil {
		return nil, err
	}

	auditLogWriter := audit.NewLogWriter(cfg.AuditLogPath, cfg.AuditLevel, cfg.AuditLogMaxage, cfg.AuditLogMaxbackup, cfg.AuditLogMaxsize)

	// NOTE(JamLee): 启动的 server 中包含了所有http路径与之对应的 handler 挂载。这里的 server 的含义是 route （来自 mux），handler 就是 root route， 还需要一个 http 服务器。
	//  里面 server.New 对 scaledContext 的 controller 添加 handler。
	authMiddleware, handler, err := server.Start(ctx, localClusterEnabled(*cfg), scaledContext, clusterManager, auditLogWriter, rbac.NewAccessControlHandler())
	if err != nil {
		return nil, err
	}

	if os.Getenv("CATTLE_PROMETHEUS_METRICS") == "true" {
		metrics.Register(ctx, scaledContext)
	}

	if auditLogWriter != nil {
		go func() {
			<-ctx.Done()
			auditLogWriter.Output.Close()
		}()
	}

	rancher := &Rancher{
		AccessSetLookup: wranglerContext.ASL,
		Config:          *cfg,
		Handler:         handler, // NOTE(JamLee): 可供挂载的 router
		Auth:            authMiddleware,
		ScaledContext:   scaledContext,
		WranglerContext: wranglerContext,
		ClusterManager:  clusterManager,
	}

	return rancher, nil
}

// NOTE(JamLee): ListenAndServe阶段，这里启动了所有的controller
func (r *Rancher) Start(ctx context.Context) error {
	// NOTE(JamLee): 目前为止，r.ScaleContext 里的 controller，但是 server.New 方法中注册了 部分handler 上去了
	if err := r.ScaledContext.Start(ctx); err != nil {
		return err
	}
	// NOTE(JamLee): Added
	logrus.Traceln("====> ScaledContext Started")

	if err := r.WranglerContext.Start(ctx); err != nil {
		return err
	}
	// NOTE(JamLee): Added
	logrus.Traceln("====> WranglerContext Started")

	if dm := os.Getenv("CATTLE_DEV_MODE"); dm == "" {
		if err := jailer.CreateJail("driver-jail"); err != nil {
			return err
		}

		if err := cron.StartJailSyncCron(r.ScaledContext); err != nil {
			return err
		}
	}

	// NOTE(JamLee): 选举到 leader 则执行里面的函数
	go leader.RunOrDie(ctx, "", "cattle-controllers", r.ScaledContext.K8sClient, func(ctx context.Context) {
		if r.ScaledContext.PeerManager != nil {
			r.ScaledContext.PeerManager.Leader()
		}

		management, err := r.ScaledContext.NewManagementContext()
		if err != nil {
			panic(err)
		}

		// NOTE(JamLee): pkg/controllers/management, 把 handler 注册到 norman 流派的 controller 上
		managementController.Register(ctx, management, r.ScaledContext.ClientGetter.(*clustermanager.Manager))
		if err := management.Start(ctx); err != nil {
			panic(err)
		}

		// NOTE(JamLee): 把 handler 注册到 wrangle 流派的 controller 上。WranglerContext.Start(ctx) 又被 start 了一次了
		managementController.RegisterWrangler(ctx, r.WranglerContext, management, r.ScaledContext.ClientGetter.(*clustermanager.Manager))
		if err := r.WranglerContext.Start(ctx); err != nil {
			panic(err)
		}

		if err := addData(management, r.Config); err != nil {
			panic(err)
		}

		if err := telemetry.Start(ctx, r.Config.HTTPSListenPort, r.ScaledContext); err != nil {
			panic(err)
		}

		tokens.StartPurgeDaemon(ctx, management)
		cronTime := settings.AuthUserInfoResyncCron.Get()
		maxAge := settings.AuthUserInfoMaxAgeSeconds.Get()
		providerrefresh.StartRefreshDaemon(ctx, r.ScaledContext, management, cronTime, maxAge)
		cleanupOrphanedSystemUsers(ctx, management)
		logrus.Infof("Rancher startup complete")

		<-ctx.Done()
	})

	// NOTE(JamLee): 原来 steve 是新的 dashboard 功能。初步分析时不要开启它。
	if features.Steve.Enabled() {
		handler, err := newSteve(ctx, r)
		if err != nil {
			return err
		}
		r.Handler = handler
	}

	return nil
}

// NOTE(JamLee): 在 leader 上继续充实 management 的内容。
func addData(management *config.ManagementContext, cfg Config) error {
	adminName, err := addRoles(management)
	if err != nil {
		return err
	}

	if localClusterEnabled(cfg) {
		if err := addLocalCluster(cfg.Embedded, adminName, management); err != nil {
			return err
		}
	} else if cfg.AddLocal == "false" {
		if err := removeLocalCluster(management); err != nil {
			return err
		}
	}

	if err := addAuthConfigs(management); err != nil {
		return err
	}

	if err := syncCatalogs(management); err != nil {
		return err
	}

	if err := addSetting(); err != nil {
		return err
	}

	if err := addDefaultPodSecurityPolicyTemplates(management); err != nil {
		return err
	}

	if err := addKontainerDrivers(management); err != nil {
		return err
	}

	if err := addCattleGlobalNamespaces(management); err != nil {
		return err
	}

	return addMachineDrivers(management)
}

func localClusterEnabled(cfg Config) bool {
	if cfg.AddLocal == "true" || (cfg.AddLocal == "auto" && !cfg.Embedded) {
		return true
	}
	return false
}

// NOTE(JamLee): 新版的Dashboard
func newSteve(ctx context.Context, rancher *Rancher) (http.Handler, error) {
	clusterapiServer := &clusterapi.Server{}
	cfg := steveserver.Server{
		AccessSetLookup: rancher.AccessSetLookup,
		Controllers:     rancher.WranglerContext.Controllers,
		RestConfig:      steveserver.RestConfigDefaults(&rancher.ScaledContext.RESTConfig),
		AuthMiddleware:  rancher.Auth,
		Next:            rancher.Handler,
		StartHooks: []steveserver.StartHook{
			func(ctx context.Context, server *steveserver.Server) error {
				return steve.Setup(server, rancher.WranglerContext, localClusterEnabled(rancher.Config), rancher.Handler)
			},
			clusterapiServer.Setup,
		},
	}

	localSteve, err := cfg.Handler(ctx)
	if err != nil {
		return nil, err
	}

	return clusterapiServer.Wrap(localSteve), nil
}
