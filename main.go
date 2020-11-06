package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"syscall"

	"github.com/docker/docker/pkg/reexec"
	"github.com/ehazlett/simplelog"
	_ "github.com/rancher/norman/controller"
	"github.com/rancher/norman/pkg/dump"
	"github.com/rancher/norman/pkg/kwrapper/k8s"
	"github.com/rancher/rancher/app"
	"github.com/rancher/rancher/pkg/logserver"
	"github.com/rancher/rancher/pkg/version"
	"github.com/rancher/wrangler/pkg/signals"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

var (
	profileAddress = "localhost:6060"
	kubeConfig     string
)

func main() {
	// NOTE(JamLee): 注册重置密码的 bin 文件， 确认默认的 admin 用户
	app.RegisterPasswordResetCommand()
	app.RegisterEnsureDefaultAdminCommand()

	// NOTE(JamLee): 调用的是 reexec.init()，其作用是实现类似busybox的程序调用，即根据文件名 决定程序的功能。这里暂且不表，
	//  有兴趣的同学可以自行研究reexec package的源码，代码位于$SRC/reexec目录下。
	if reexec.Init() {
		return
	}

	os.Unsetenv("SSH_AUTH_SOCK")
	os.Unsetenv("SSH_AGENT_PID")
	os.Setenv("DISABLE_HTTP2", "true")

	if dm := os.Getenv("CATTLE_DEV_MODE"); dm != "" {
		if dir, err := os.Getwd(); err == nil {
			dmPath := filepath.Join(dir, "management-state", "bin")
			os.MkdirAll(dmPath, 0700)
			newPath := fmt.Sprintf("%s%s%s", dmPath, string(os.PathListSeparator), os.Getenv("PATH"))

			os.Setenv("PATH", newPath)
		}
	} else {
		newPath := fmt.Sprintf("%s%s%s", "/opt/drivers/management-state/bin", string(os.PathListSeparator), os.Getenv("PATH"))
		os.Setenv("PATH", newPath)
	}

	var config app.Config

	app := cli.NewApp()
	app.Version = version.FriendlyVersion()
	app.Usage = "Complete container management platform"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:        "kubeconfig",
			Usage:       "Kube config for accessing k8s cluster",
			EnvVar:      "KUBECONFIG",
			Destination: &kubeConfig,
		},
		cli.BoolFlag{
			Name:        "debug",
			Usage:       "Enable debug logs",
			Destination: &config.Debug,
		},
		cli.BoolFlag{
			Name:        "trace",
			Usage:       "Enable trace logs",
			Destination: &config.Trace,
		},
		cli.StringFlag{
			Name:        "add-local",
			Usage:       "Add local cluster (true, false, auto)",
			Value:       "auto",
			Destination: &config.AddLocal,
		},
		cli.IntFlag{
			Name:        "http-listen-port",
			Usage:       "HTTP listen port",
			Value:       8080,
			Destination: &config.HTTPListenPort,
		},
		cli.IntFlag{
			Name:        "https-listen-port",
			Usage:       "HTTPS listen port",
			Value:       8443,
			Destination: &config.HTTPSListenPort,
		},
		cli.StringFlag{
			Name:        "k8s-mode",
			Usage:       "Mode to run or access k8s API server for management API (embedded, external, auto)",
			Value:       "auto",
			Destination: &config.K8sMode,
		},
		cli.StringFlag{
			Name:  "log-format",
			Usage: "Log formatter used (json, text, simple)",
			Value: "simple",
		},
		cli.StringSliceFlag{
			Name:   "acme-domain",
			EnvVar: "ACME_DOMAIN",
			Usage:  "Domain to register with LetsEncrypt",
			Value:  &config.ACMEDomains,
		},
		cli.BoolFlag{
			Name:        "no-cacerts",
			Usage:       "Skip CA certs population in settings when set to true",
			Destination: &config.NoCACerts,
		},
		cli.StringFlag{
			Name:        "audit-log-path",
			EnvVar:      "AUDIT_LOG_PATH",
			Value:       "/var/log/auditlog/rancher-api-audit.log",
			Usage:       "Log path for Rancher Server API. Default path is /var/log/auditlog/rancher-api-audit.log",
			Destination: &config.AuditLogPath,
		},
		cli.IntFlag{
			Name:        "audit-log-maxage",
			Value:       10,
			EnvVar:      "AUDIT_LOG_MAXAGE",
			Usage:       "Defined the maximum number of days to retain old audit log files",
			Destination: &config.AuditLogMaxage,
		},
		cli.IntFlag{
			Name:        "audit-log-maxbackup",
			Value:       10,
			EnvVar:      "AUDIT_LOG_MAXBACKUP",
			Usage:       "Defines the maximum number of audit log files to retain",
			Destination: &config.AuditLogMaxbackup,
		},
		cli.IntFlag{
			Name:        "audit-log-maxsize",
			Value:       100,
			EnvVar:      "AUDIT_LOG_MAXSIZE",
			Usage:       "Defines the maximum size in megabytes of the audit log file before it gets rotated, default size is 100M",
			Destination: &config.AuditLogMaxsize,
		},
		cli.IntFlag{
			Name:        "audit-level",
			Value:       0,
			EnvVar:      "AUDIT_LEVEL",
			Usage:       "Audit log level: 0 - disable audit log, 1 - log event metadata, 2 - log event metadata and request body, 3 - log event metadata, request body and response body",
			Destination: &config.AuditLevel,
		},
		cli.StringFlag{
			Name:        "profile-listen-address",
			Value:       "127.0.0.1:6060",
			Usage:       "Address to listen on for profiling",
			Destination: &profileAddress,
		},
		cli.StringFlag{
			Name:        "features",
			EnvVar:      "CATTLE_FEATURES",
			Value:       "",
			Usage:       "Declare specific feature values on start up. Example: \"kontainer-driver=true\" - kontainer driver feature will be enabled despite false default value",
			Destination: &config.Features,
		},
	}

	// NOTE(JamLee): rancher命令行不论带上什么参数，这里都是入口
	app.Action = func(c *cli.Context) error {
		// enable profiler
		if profileAddress != "" {
			go func() {
				log.Println(http.ListenAndServe(profileAddress, nil))
			}()
		}
		// NOTE(JamLee):初始化日志对象, 选择 json, text 等对象格式
		initLogs(c, config)
		return run(c, config)
	}

	app.ExitErrHandler = func(c *cli.Context, err error) {
		logrus.Fatal(err)
	}

	app.Run(os.Args)
}

func initLogs(c *cli.Context, cfg app.Config) {
	switch c.String("log-format") {
	case "simple":
		logrus.SetFormatter(&simplelog.StandardFormatter{})
	case "text":
		logrus.SetFormatter(&logrus.TextFormatter{})
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{})
	}
	logrus.SetOutput(os.Stdout)
	if cfg.Debug {
		logrus.SetLevel(logrus.DebugLevel)
		logrus.Debugf("Loglevel set to [%v]", logrus.DebugLevel)
	}
	if cfg.Trace {
		logrus.SetLevel(logrus.TraceLevel)
		logrus.Tracef("Loglevel set to [%v]", logrus.TraceLevel)
	}

	logserver.StartServerWithDefaults()
}

func migrateETCDlocal() {
	if _, err := os.Stat("etcd"); err != nil {
		return
	}

	// Purposely ignoring errors
	os.Mkdir("management-state", 0700)
	os.Symlink("../etcd", "management-state/etcd")
}

// NOTE(JamLee): 程序入口
func run(cli *cli.Context, cfg app.Config) error {
	logrus.Infof("Rancher version %s is starting", version.FriendlyVersion())
	logrus.Infof("Rancher arguments %+v", cfg)

	// NOTE(JamLee): 输出 gorouetine
	//  https://github.com/maruel/panicparse/
	//  例如: kill -SIGUSR1 34068
	dump.GoroutineDumpOn(syscall.SIGUSR1, syscall.SIGILL)

	// NOTE(JamLee): 信号处理器中可以取消的 ctx
	ctx := signals.SetupSignalHandler(context.Background())

	// NOTE(JamLee): 创建本地 Rancher 缓存数据
	migrateETCDlocal()

	// NOTE(JamLee): 在 mac 上执行，不是 embedded 的
	embedded, clientConfig, err := k8s.GetConfig(ctx, cfg.K8sMode, kubeConfig)
	if err != nil {
		return err
	}
	cfg.Embedded = embedded

	// NOTE(JamLee): 首先启动全局的 app 对象
	os.Unsetenv("KUBECONFIG")
	server, err := app.New(ctx, clientConfig, &cfg)
	if err != nil {
		return err
	}

	return server.ListenAndServe(ctx)
}
